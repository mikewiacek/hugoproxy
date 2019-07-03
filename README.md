
# Hugoproxy

Stephen Mann has a great [blog post](https://stephenmann.io/post/hosting-a-hugo-site-in-a-google-bucket/) written about how to serve a static Hugo website from a GCS bucket.

I really liked this approach, but it means you can't get TLS - which is important these days.

Hugoproxy is a really small reverse SSL/TLS terminating proxy written in Go.

If you follow steps 1, 2, and 3 from his blog post ([link](https://stephenmann.io/post/hosting-a-hugo-site-in-a-google-bucket/)) you are practically all of the way there.

On a GCE micro instance (cost: [Free](https://cloud.google.com/free/)) you can run this little server to give your blog HTTPS for free courtesy of [Let's Encrypt](https://letsencrypt.org).

Using his examples... with a few modifications, it's easy to do.

1. Use a different hostname and bucket name than what you want users to see in their browser. In his case his blog (and his GCS bucket) was example.stephenmann.io. So what you'd do is something like follow his steps 2 and 3 but use a different name for internal purposes like example-internal.stephenmann.io as your DNS and bucket name (N.B. They must match).

2. Then setup your DNS for example.stephenmann.io (notice no -internal) to point to the IP address of your GCE micro instance.

3. Make sure you GCE micro instance has scopes setup for GCP Cloud Datastore and GCP Cloud Datastore is enabled in your GCP project.

4. Compile and run hugoproxy.  In this example you'd run hugo proxy like this: 
	```bash
	$ sudo hugoproxy --blog_hostname=example.stephenmann.io --gcs_bucket=example-internal.stephenmann.io
	```

5. Sit back and try to visit https://example.stephenmann.io in your browser and see the TLS magic happen. All certificates are fetched automatically and cached in GCP Cloud Datastore.

-----
I threw these instructions together really quickly. I assume you know a little bit about GCP and Go. Compiling hugoproxy is pretty straight forward. Let me know if you'd like more detailed instructions.

Stephen did a great job of showing how to make a simple blog, gsutil rsync it to a bucket, and setup the bucket to serve external traffic via GCS's native built in HTTP serving of bucket contents. Thank you, Stephen!

