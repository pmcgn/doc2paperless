# Run the Application

```
docker build -t doc2paperless .
docker run -e WATCH_DIR=/path/to/watch -e PAPERLESS_BASE_URL=http://paperless-ngx -p 8080:8080 doc2paperless
```

# Development

Build the application:
docker build -t doc2paperless .

Run:
docker run -p 8080:8080  docker.io/library/doc2paperless