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

# Get git information for build
RUN git rev-parse --short=8 HEAD > /tmp/git_hash.txt && \
    git log -1 --format=%ci | head -c 16 > /tmp/git_date.txt

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o nostremail .

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates sqlite

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/nostremail .

# Copy git information from builder stage
COPY --from=builder /tmp/git_hash.txt /tmp/git_date.txt /tmp/

# Copy config example and templates
COPY --from=builder /app/config.json.example .
COPY --from=builder /app/templates/ ./templates/

# Create directory for SQLite database and ensure processed_notes.db exists
RUN mkdir -p /data && touch /root/processed_notes.db

# Expose port (if needed for health checks)
EXPOSE 8080

# Set environment variables
ENV GIN_MODE=release

# Set git information from build
RUN GIT_HASH=$(cat /tmp/git_hash.txt 2>/dev/null || echo "unknown") && \
    GIT_DATE=$(cat /tmp/git_date.txt 2>/dev/null || echo "unknown") && \
    echo "export GIT_COMMIT_HASH=$GIT_HASH" >> /root/.bashrc && \
    echo "export GIT_COMMIT_DATE=$GIT_DATE" >> /root/.bashrc

# Run the application
CMD ["sh", "-c", "GIT_HASH=$(cat /tmp/git_hash.txt 2>/dev/null || echo 'unknown') && GIT_DATE=$(cat /tmp/git_date.txt 2>/dev/null || echo 'unknown') && export GIT_COMMIT_HASH=$GIT_HASH && export GIT_COMMIT_DATE=$GIT_DATE && ./nostremail --nostr-listen"]
