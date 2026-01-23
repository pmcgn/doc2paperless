# doc2paperless

A lightweight service that monitors a folder for scanned documents and automatically uploads them to your Paperless-ngx server. Handles slow scanners and file locks gracefully.

## Quick Start

```bash
docker run -d \
  --name doc2paperless \
  -e PAPERLESS_BASE_URL="http://your-paperless-url:8000" \
  -e PAPERLESS_AUTH_TOKEN="your-auth-token-here" \
  -v /path/to/scanner/folder:/consumefolder \
  -p 2112:2112 \
  pmcgn/doc2paperless:latest
```

## What Does It Do?

`doc2paperless` watches a folder for new files from your scanner and uploads them to Paperless-ngx. It's specifically designed to handle:

- **Slow scanners** that write files gradually over time
- **File locks** from scanners that keep files open while writing
- **Batch scanning** with multiple files arriving simultaneously
- **Remote Paperless servers** where the scanner can't reach paperless-ngx directly

### How It Works

1. **Monitors** the consume folder for new files
2. **Waits** until files are completely written and no longer locked
3. **Uploads** stable files to your Paperless-ngx server
4. **Deletes** local files after successful upload

> **⚠️ WARNING:** Files are permanently deleted from the consume folder after successful upload to Paperless-ngx. This does not guarantee a successful import.

## Use Cases

Use `doc2paperless` when:

- Your Paperless-ngx server runs remotely (e.g., in the cloud) and your scanner can't upload directly to it
- You experience issues with Paperless-ngx's built-in consume folder polling
- Your scanner writes files slowly or keeps them locked during scanning

> **⚠️ NOTE:** Do not run `doc2paperless` and Paperless-ngx on the same machine watching the same folder. They use identical file-watching mechanisms and will conflict.

## Configuration

All configuration is done through environment variables.

### Required Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `PAPERLESS_BASE_URL` | Base URL of your Paperless-ngx server | `https://paperless.example.com:8000` |
| `PAPERLESS_AUTH_TOKEN` | Authentication token from Paperless-ngx | `281298728b981fb7c86d14a77f85e686974e6c4c` |

### Optional Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CONSUME_FOLDER` | Directory to watch for new files | `/consumefolder` |
| `FILE_CONSUME_WHITELIST` | File patterns to process (comma-separated) | `*.pdf` |
| `FILE_STABILITY_CHECK_INTERVAL_SECONDS` | Time between stability checks | `2s` |
| `FILE_STABILITY_CHECK_COUNT` | Number of stable checks required before upload | `5` |
| `HTTP_UPLOAD_RETRY_DELAY_SECONDS` | Delay between upload retry attempts | `5s` |
| `MAX_CONCURRENT_UPLOADS` | Maximum simultaneous uploads to Paperless | `1` |
| `MAX_CONCURRENT_STABILITY_CHECKS` | Maximum files being checked simultaneously | `10` |
| `VERBOSE` | Enable detailed logging | `false` |
| `TZ` | Timezone for the application | `Europe/Berlin` |

### Getting Your Auth Token

To get your Paperless-ngx authentication token:

1. Log in to your Paperless-ngx web interface
2. Go to **Settings** → **Django Adminpanel** → **Tokens** (or navigate to `/admin/authtoken/tokenproxy/`)
3. Create a new token or copy an existing one
4. Use this token for the `PAPERLESS_AUTH_TOKEN` environment variable

### File Stability Detection

`doc2paperless` ensures files are completely written before uploading:

- Checks file size every `FILE_STABILITY_CHECK_INTERVAL_SECONDS` (default: 2 seconds)
- Verifies the file is not locked by attempting to open it
- Requires `FILE_STABILITY_CHECK_COUNT` consecutive successful checks (default: 5)
- **Default behavior**: File must be stable and unlocked for 10 seconds (5 checks × 2s) before upload

**Example**: With defaults, a 10 MB file from a slow scanner will be checked every 2 seconds. Once the file stops growing and is no longer locked for 5 consecutive checks (10 seconds total), it will be uploaded.

### Batch Scanning

When multiple files arrive simultaneously:

- `MAX_CONCURRENT_STABILITY_CHECKS` limits how many files are checked at once (default: 10)
- `MAX_CONCURRENT_UPLOADS` limits simultaneous uploads to Paperless (default: 1)
- All files are queued reliably - nothing gets lost
- Sequential uploads (default) prevent overwhelming your Paperless server

## Docker Deployment

### Basic Example

```bash
docker run -d \
  --name doc2paperless \
  --restart unless-stopped \
  -e PAPERLESS_BASE_URL="http://192.168.1.100:8000" \
  -e PAPERLESS_AUTH_TOKEN="your-token-here" \
  -v /mnt/scanner:/consumefolder \
  -p 2112:2112 \
  pmcgn/doc2paperless:latest
```

### Advanced Example with Custom Settings

```bash
docker run -d \
  --name doc2paperless \
  --restart unless-stopped \
  -e PAPERLESS_BASE_URL="https://paperless.example.com" \
  -e PAPERLESS_AUTH_TOKEN="your-token-here" \
  -e FILE_CONSUME_WHITELIST="*.pdf,*.jpg,*.png" \
  -e FILE_STABILITY_CHECK_COUNT="3" \
  -e FILE_STABILITY_CHECK_INTERVAL_SECONDS="5s" \
  -e MAX_CONCURRENT_UPLOADS="2" \
  -e VERBOSE="true" \
  -e TZ="America/New_York" \
  -v /mnt/scanner:/consumefolder \
  -p 2112:2112 \
  pmcgn/doc2paperless:latest
```

### Docker Compose

```yaml
version: '3.8'

services:
  doc2paperless:
    image: pmcgn/doc2paperless:latest
    container_name: doc2paperless
    restart: unless-stopped
    environment:
      - PAPERLESS_BASE_URL=http://paperless:8000
      - PAPERLESS_AUTH_TOKEN=your-token-here
      - FILE_CONSUME_WHITELIST=*.pdf,*.jpg,*.png
      - TZ=Europe/Berlin
    volumes:
      - /path/to/scanner/folder:/consumefolder
    ports:
      - "2112:2112"
```

## Monitoring

### Prometheus Metrics

Metrics are exposed at `http://localhost:2112/metrics` in Prometheus format:

- `successful_uploads` - Total number of successful uploads
- `failed_uploads` - Total number of failed upload attempts
- `upload_retries` - Total number of upload retries

### Health Checks

Health endpoints are available at:

- **Liveness**: `http://localhost:2112/health/liveness`
- **Readiness**: `http://localhost:2112/health/readiness`

Both endpoints return HTTP 200 with body `OK` when healthy.

## Troubleshooting

### Files Not Being Uploaded

**Check file patterns**: Ensure your files match the `FILE_CONSUME_WHITELIST` pattern
```bash
# View current whitelist setting
docker logs doc2paperless | grep "Configuration loaded"

# Common patterns
*.pdf           # PDF files only
*.pdf,*.jpg     # PDF and JPG files
*               # All files
```

**Check file stability**: Enable verbose logging to see stability checks
```bash
docker run -d \
  -e VERBOSE="true" \
  ... other options ...
  pmcgn/doc2paperless:latest

# View logs
docker logs -f doc2paperless
```

### Files Stuck in "Checking Stability"

If files remain in the folder without being uploaded:

1. **Scanner still has the file open** - Wait for the scanner to finish and release the file
2. **File is still growing** - The scanner may be writing very slowly
3. **Permissions issue** - Check that `doc2paperless` can read and delete files in the folder

```bash
# Check file permissions
docker exec doc2paperless ls -la /consumefolder
```

### Upload Failures

**Check Paperless connectivity**:
```bash
# Test connection from container
docker exec doc2paperless wget -O- http://your-paperless-url:8000/api/
```

**Check authentication token**:
- Verify the token is valid in Paperless-ngx admin interface
- Ensure the token has appropriate permissions

**Check Paperless logs**:
```bash
# Check for errors on the Paperless side
docker logs paperless
```

### Files Being Deleted Without Upload

If files disappear from the consume folder:

1. Check `failed_uploads` and `upload_retries` metrics at `http://localhost:2112/metrics`
2. Review logs for upload errors: `docker logs doc2paperless`
3. Verify the file matched your whitelist pattern

> **Important**: Files are only deleted after a successful HTTP 200 response from Paperless-ngx.

## Multiple File Types

To accept multiple file types, use comma-separated patterns:

```bash
-e FILE_CONSUME_WHITELIST="*.pdf,*.jpg,*.jpeg,*.png,*.tiff,*.txt"
```

Common patterns:
- `*.pdf` - PDF documents
- `*.jpg,*.jpeg,*.png` - Images
- `*.pdf,*.jpg,*.png,*.tiff` - Documents and scanned images
- `*` - All files (use with caution)

## Performance Tuning

### For Slow Scanners

Increase stability check parameters:
```bash
-e FILE_STABILITY_CHECK_INTERVAL_SECONDS="5s"
-e FILE_STABILITY_CHECK_COUNT="6"
# File must be stable for 30 seconds (6 × 5s)
```

### For Fast Scanners

Decrease stability check parameters:
```bash
-e FILE_STABILITY_CHECK_INTERVAL_SECONDS="1s"
-e FILE_STABILITY_CHECK_COUNT="3"
# File must be stable for 3 seconds (3 × 1s)
```

### For Batch Scanning

Increase concurrent processing:
```bash
-e MAX_CONCURRENT_STABILITY_CHECKS="20"
-e MAX_CONCURRENT_UPLOADS="3"
# Check up to 20 files simultaneously
# Upload up to 3 files at once
```