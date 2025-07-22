# doc2paperless

`doc2paperless` is a service designed to monitor a directory for new files and upload them to a Paperless-ngx server. It uses the same functionality for receiving new file events as Paperless-ngx. It helps if the Paperless-ngx instance is not running on the machine, where the files are uploaded by the scanner. 

There are two Use-Cases:
- Paperless-ngx does not run on the machine, where the files are being uploaded to (e.g. Cloud Provider).
- You have issues with the internal polling mechanism of Paperless-ngx.

<span style="color:red">NOTE: doc2paperless uses the same mechanism to get informed about new files like Paperless-ngx. It does not make sense to install both on the same machine</span>.

## Environment Variables

The following table outlines the environment variables that can be used to configure `doc2paperless`:

| Variable Name                            | Description                                                          | Default Value <br> Example             | Required  |
|------------------------------------------|----------------------------------------------------------------------|----------------------------|-----------|
| `PAPERLESS_BASE_URL`                     | Base URL of the Paperless server.                                    |*not set*<br>`https://my-paperless.mydomain.com:8000`    | Yes       |
| `PAPERLESS_AUTH_TOKEN`                   | Authentication token for the Paperless server.                       | *not set*<br>`281298728b981fb7c86d14a77f85e686974e6c4c` | Yes       |
| `CONSUME_FOLDER`                         | Directory path to watch for new files. THERE IS NO NEED TO CHANGE THIS! Make sure to mount the correct hosth path into this folder   | `/consumefolder`           | No       |
| `HTTP_UPLOAD_RETRY_DELAY_SECONDS`        | Delay between upload retries.                                        | `5s`                       | No        |
| `FILE_STABILITY_CHECK_INTERVAL_SECONDS`  | Interval between file stability checks.                              | `10s`                      | No        |
| `FILE_STABILITY_CHECK_COUNT`             | Number of times to check file stability before upload.               | `3`                        | No        |
| `FILE_CONSUME_WHITELIST`                 | Whitelist of file types to be pushed. Comma Separated List.          | `*.pdf`<br>`*.pdf,*.txt`                     | No        |
| `TZ`                                     | Timezone for the application.                                        | `Europe/Berlin`             | No        |

There is a mechanis in place (like in paperless-ngx), to detect if a file is completely uploaded by the Scanner. Before a file is pushed to Paperless-ngx, the filesize must not change over multiple consecutive checks. `doc2paperless` will check the filesize every `FILE_STABILITY_CHECK_INTERVAL_SECONDS`. If the size has not changed for `FILE_STABILITY_CHECK_COUNT` cycles, it will upload and delete (!) the file.

## Prometheus Metrics

For more insights, `doc2paperless` provides a metrics endpoint at `/metrics` via port `2112`. The Values are prometheus compatible.

Currently, the following additional values are available:
- `successful_uploads`: Number of successful uploads.
- `failed_uploads`: Number of failed uploads.
- `upload_retries`: Number of upload retries.


## Health Endpoint

For liveness and readiness checks, the application provides the endpoints `/health/readiness` and `/health/liveness` via port `2112`.

Both endpoints respond with HTTP 200 and the string `OK` as Body. For now, there are no fancy checks in place.

## Start the application via docker run

Here's an example command to run the service in a Docker container, setting all required environment variables:

```bash
docker run -d \
  --name doc2paperless \
  -e PAPERLESS_BASE_URL="http://your-paperless-url:8000" \
  -e PAPERLESS_AUTH_TOKEN="your-auth-token" \
  -e TZ="Europe/Berlin" \
  -v /your/local/scanner/folder:/consumefolder \
  -p 2112:2112 \
  pmcgn/doc2paperless:latest
```

Replace the placeholders with your actual configuration values. This command will start the service in detached mode, configure the environment variables, and expose the Prometheus metrics and health endpoints on port 2112. 