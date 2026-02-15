.PHONY: server

server:
	air \
		--build.cmd "go build -o tmp/bin/main ./cmd/main.go" \
		--build.bin "tmp/bin/main" \
		--build.delay "100" \
		--build.exclude_dir "node_modules" \
		--build.include_ext "go" \
		--build.stop_on_error "false" \
		--misc.clean_on_exit true

dev:
	make server

