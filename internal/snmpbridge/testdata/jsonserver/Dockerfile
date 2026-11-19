FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
WORKDIR /app
COPY main.go ./
RUN CGO_ENABLED=0 go build -o /jsonserver main.go

FROM scratch
COPY --from=builder /jsonserver /jsonserver
ENTRYPOINT ["/jsonserver"]
