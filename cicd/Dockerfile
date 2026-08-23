# OpenRouter Spending Cockpit — image built by CI (.github/workflows/main.yml).
# Version tag comes from the VERSION file at the repo root.

FROM golang:1.26.5 AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orcmon .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/orcmon /usr/local/bin/orcmon
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/orcmon"]
