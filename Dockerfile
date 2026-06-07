FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -o /out/vetka-backend-panel ./cmd/server

FROM alpine:3.20
RUN adduser -D -H vetka
WORKDIR /app
COPY --from=build /out/vetka-backend-panel /app/vetka-backend-panel
USER vetka
EXPOSE 8080
ENTRYPOINT ["/app/vetka-backend-panel"]
