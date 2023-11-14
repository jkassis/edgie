
.PHONY: build
build:
	export CGO_CFLAGS_ALLOW='.*'
	export CGO_CPPFLAGS="-Wno-error -Wno-nullability-completeness -Wno-expansion-to-defined -Wno-builtin-requires-header"
	go build -o server

.PHONY: run
run:
	export AWS_S3_BUCKET_NAME=farmgoods
	export CGO_CFLAGS_ALLOW='.*'
	export CGO_CPPFLAGS="-Wno-error -Wno-nullability-completeness -Wno-expansion-to-defined -Wno-builtin-requires-header"
	go run server.go
