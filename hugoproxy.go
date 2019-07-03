// Command hugoproxy serves a website from a GCS bucket using an HTTPS
// front end with automatic certificates provided by LetsEncrypt. It pulls
// the content of the bucket via a GCS bucket's built in HTTP serving. As
// it pulls the data over an unencrypted connection, it should only be run
// from a network that's considered secure. In this case, it should ideally
// run from GCE so the end to end path to GCS is already somewhat trusted.
//
// TODO: Pull files from GCS and serve them directly and not rely on GCS's
// insecure HTTP server.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/datastore"
	log "github.com/golang/glog"
	"github.com/mikewiacek/flags"
	"golang.org/x/crypto/acme/autocert"
)

var (
	project    = flag.String("gcp_project", "", "GCP Cloud Datastore used for certificate caching (if on GCE this is determined automatically and can be left blank)")
	hostnames  = flags.StringSlice("blog_hostnames", []string{}, "CSV of hostnames for which to get certificates")
	hugoBucket = flag.String("gcs_bucket", "", "name of the GCS bucket storing our site")
)

// DSCache implements autocert.Cache against GCP Cloud Datastore.
type DSCache struct {
	D *datastore.Client
}

// CachedCertificate is how we cache certificates and letsencrypt keys in GCP Cloud Datastore.
type CachedCertificate struct {
	Certificate []byte `datastore:",noindex"`
}

// Get reads a certificate data with the provided name from GCP Cloud Datastore cache.
func (d *DSCache) Get(ctx context.Context, name string) ([]byte, error) {
	cached := &CachedCertificate{}
	key := datastore.NameKey("CachedCertificate", name, nil)
	if err := d.D.Get(ctx, key, cached); err != nil {
		if err == datastore.ErrNoSuchEntity {
			log.Infof("datastore cache miss for certificate: %s", name)
			return nil, autocert.ErrCacheMiss
		}
		log.Errorf("Error fetching cached cert with name %s from datastore: %v", name, err)
		return nil, err
	}

	log.V(2).Infof("Cache hit for certificate with name: %s", name)
	return cached.Certificate, nil
}

// Put writes the certificate data for the specified name to GCP Cloud Datastore cache.
func (d *DSCache) Put(ctx context.Context, name string, data []byte) error {
	key := datastore.NameKey("CachedCertificate", name, nil)
	_, err := d.D.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		cached := &CachedCertificate{}
		if err := tx.Get(key, cached); err != nil && err != datastore.ErrNoSuchEntity {
			return err
		}

		// Don't update if the current value is what we're storing is the same.
		if bytes.Equal(data, cached.Certificate) {
			return nil
		}

		cached.Certificate = data

		_, err := tx.Put(key, cached)
		return err
	})
	if err != nil {
		log.Errorf("Error storing certificate with name %s in datastore: %v", name, err)
		return err
	}
	log.V(2).Infof("Successfully stored certificate with name %s in datastore", name)
	return nil
}

// Delete removes then entry with name from the GCP Cloud Datastore backed cache.
func (d *DSCache) Delete(ctx context.Context, name string) error {
	return d.D.Delete(ctx, datastore.NameKey("CachedCertificate", name, nil))
}

// goSecure just sends folks to the HTTPS version of whatever they requested.
func goSecure(w http.ResponseWriter, r *http.Request) {
	r.URL.Scheme = "https"
	r.URL.Host = r.Host
	http.Redirect(w, r, r.URL.String(), http.StatusMovedPermanently)
}

// Taken from: golang.org/src/net/http/httputil/reverseproxy.go
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

type transport struct {
	http.RoundTripper
}

// RoundTrip implements http.RoundTripper on transport. It's necessary because sometimes
// GCS will return a 301/302 to an actual index.html file if a 'directory' is requested
// instead. This will leak the existence of the underlying bucket. This RoundTrip function
// will look for 301/302 redirects and rewrite the redirected URL to maintain the appropriate
// user visible hostname.
func (t *transport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	if resp, err = t.RoundTripper.RoundTrip(req); err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		loc := resp.Header.Get("Location")
		locURL, err := url.Parse(loc)
		if err != nil {
			return nil, err
		}
		locURL.Host = req.Header.Get("X-Original-Host")
		locURL.Scheme = "https"
		resp.Header.Set("Location", locURL.String())
		log.V(2).Infof("Rewrote redirected URL from %s to %s", loc, locURL)
	}

	return resp, nil
}

// NewSingleHostReverseProxy is a copy of httputil.NewSingleHostReverseProxy but it
// is modified to set the request.Host header of the modified request to match the
// hostname of target.
func NewSingleHostReverseProxy(target *url.URL) *httputil.ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
		req.Header.Set("X-Original-Host", req.Host)
		req.Host = target.Host
	}

	return &httputil.ReverseProxy{Director: director, Transport: &transport{http.DefaultTransport}}
}

func main() {
	flag.Parse()

	ctx := context.Background()

	if *project == "" {
		p, err := metadata.ProjectID()
		if err != nil {
			log.Exitf("metadata.ProjectID: %v", err)
		}
		*project = p
	}

	dsClient, err := datastore.NewClient(ctx, *project)
	if err != nil {
		log.Exitf("datastore.NewClient(%q): %v", *project, err)
	}
	log.Infof("Connected to datastore %q", *project)

	hugoURL, err := url.Parse(fmt.Sprintf("http://%s", strings.TrimPrefix(*hugoBucket, "gs://")))
	if err != nil {
		log.Exitf("url.Parse(http://%s): %v", *hugoBucket, err)
	}
	log.Infof("Actual site serving from: %s", hugoURL)

	m := &autocert.Manager{
		Cache:      &DSCache{dsClient},
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(*hostnames...),
	}
	s := &http.Server{
		Addr:      ":https",
		TLSConfig: m.TLSConfig(),
		Handler:   NewSingleHostReverseProxy(hugoURL),
	}

	// Redirect http requests to https...
	go func() {
		log.Info("Serving goSecure handler on port 80")
		if err := http.ListenAndServe(":http", m.HTTPHandler(http.HandlerFunc(goSecure))); err != nil {
			log.Exitf("http.ListenAndServe: %v", err)
		}
	}()

	// Now serve the TLS version of our content.
	log.Info("Serving TLS on port 443")
	if err := s.ListenAndServeTLS("", ""); err != nil {
		log.Exitf("s.ListenAndServeTLS: %v", err)
	}
}
