# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git gcc musl-dev sqlite-dev

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o nostremail .

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates sqlite

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/nostremail .

# Copy config example and templates
COPY --from=builder /app/config.json.example .
COPY --from=builder /app/templates/ ./templates/

# Create directory for SQLite database and ensure processed_notes.db exists
RUN mkdir -p /data && touch /root/processed_notes.db

# Expose port (if needed for health checks)
EXPOSE 8080

# Set environment variables
ENV GIN_MODE=release

# Run the application
CMD ["./nostremail", "--nostr-listen"]
