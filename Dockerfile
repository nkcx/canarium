FROM --platform=linux/amd64 node:22-alpine AS frontend
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend
RUN apk add --no-cache gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /app/web/dist ./web/dist
RUN CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o canarium ./cmd/canarium

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=backend /app/canarium /usr/local/bin/canarium
RUN mkdir -p /var/lib/canarium /etc/canarium
EXPOSE 8420
ENTRYPOINT ["canarium"]
CMD ["run", "--config", "/etc/canarium/config.yaml"]
