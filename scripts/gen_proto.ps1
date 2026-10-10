# Regenerates Go gRPC code from the proto contracts of every module.
# Requires protoc unpacked into tools/protoc and the Go plugins installed:
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#   go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.30.0
#   go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@v2.30.0
# Shared proto dependencies (google/api, openapiv2 options) live in third_party.
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$protoc = Join-Path $root "tools/protoc/bin/protoc.exe"
if (-not (Test-Path $protoc)) {
    throw "protoc not found at $protoc, unpack a protoc release into tools/protoc"
}

$env:PATH = "$(go env GOPATH)\bin;$env:PATH"

foreach ($module in @("services/book-service", "services/user-service", "services/loan-service", "services/ai-service")) {
    $outDir = Join-Path $root "$module/gen/go"
    $docsDir = Join-Path $root "$module/docs"
    New-Item -ItemType Directory -Force -Path $outDir, $docsDir | Out-Null

    # protoc matches the file arguments against -I prefixes literally,
    # so the contract paths have to be relative to the repository root.
    $protoFiles = Get-ChildItem -Recurse -Filter *.proto (Join-Path $root "$module/proto") |
        ForEach-Object { $_.FullName.Substring($root.Length + 1).Replace("\", "/") }

    & $protoc "-I" "$module/proto" "-I" "third_party" `
        "--go_out" "$module/gen/go" "--go_opt" "paths=source_relative" `
        "--go-grpc_out" "$module/gen/go" "--go-grpc_opt" "paths=source_relative" `
        "--grpc-gateway_out" "$module/gen/go" "--grpc-gateway_opt" "paths=source_relative,logtostderr=true" `
        "--openapiv2_out" "$module/docs" "--openapiv2_opt" "logtostderr=true" `
        $protoFiles

    if ($LASTEXITCODE -ne 0) {
        throw "protoc failed for $module"
    }

    Write-Host "generated gRPC, grpc-gateway and Swagger code for $module"
}
