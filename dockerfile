# Step 1: Build the Go application
FROM golang:1.24.4 AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy the Go module files
COPY go.mod go.sum ./

# Download the Go module dependencies
RUN go mod download

# Copy the rest of the application source code
COPY . .

# Build the Go application with CGO disabled for static linking
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o doc2paperless

# Step 2: Create a minimal container for the application
FROM alpine:3.22.0

# Set the working directory inside the container
WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/doc2paperless .

# Verify the binary is copied
RUN ls -l /app

# Create consumefolder
RUN mkdir -p /consumefolder

# Expose the port that your application listens on
EXPOSE 8000

# Command to run the application
CMD ["/app/doc2paperless"]