build:
	go build -o bin/app main.go

run:
	go run main.go

dev:
	air

client:
	go run ./client/main.go

proto-gen:
	protoc   \
	--go_out=../go-libs/services/aviator --go_opt=paths=source_relative \
	--go-grpc_out=require_unimplemented_servers=false:../go-libs/services/aviator \
	--go-grpc_opt=paths=source_relative messaging.proto

proto-clean:
	protoc-go-inject-tag -remove_tag_comment -input="../go-libs/services/**/*.pb.go"

proto: proto-gen | proto-clean

.PHONY: proto