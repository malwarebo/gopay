FROM golang:1.27.0-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o conductor .

FROM alpine:3.24

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/conductor .

EXPOSE 8080

CMD ["./conductor"]
