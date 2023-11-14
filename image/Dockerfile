FROM golang:1.18-alpine as builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY .. .

ENV CGO_CFLAGS_ALLOW='.*'
ENV CGO_CPPFLAGS="-Wno-error -Wno-nullability-completeness -Wno-expansion-to-defined -Wno-builtin-requires-header"
ENV CGO_ENABLED=0
ENV GOARCH=${TARGETARCH}
ENV GOOS=${TARGETOS:-linux}

RUN go build -a -o /tmp/server ./main.go

# Use distroless as minimal base image to package the app
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /tmp/server .
USER 65532:65532

ENTRYPOINT ["/server"]
