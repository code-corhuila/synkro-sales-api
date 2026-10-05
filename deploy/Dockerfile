FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/sales-api ./cmd/sales-api

FROM alpine:3.20
COPY --from=build /out/sales-api /usr/local/bin/sales-api
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sales-api"]
