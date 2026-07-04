FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/imapsmtpserver ./cmd/imapsmtpserver

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/imapsmtpserver /imapsmtpserver
EXPOSE 1025 1143 8025
ENTRYPOINT ["/imapsmtpserver", "-host", "0.0.0.0"]
