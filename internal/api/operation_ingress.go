package api

import (
	"github.com/fm39hz/gobroom/internal/ingress"
	"github.com/fm39hz/gobroom/internal/operations"
)

// Client ingress contracts and reusable wire codecs live outside the API
// server so statically linked modules can contribute them through pkg/provider.
type OperationIngress = ingress.ClientIngress
type IngressRoute = ingress.ClientIngressRoute
type JSONOperationIngress = ingress.JSONOperationIngress
type MultipartArtifactBinding = ingress.MultipartArtifactBinding
type MultipartOperationIngress = ingress.MultipartOperationIngress

var ErrArtifactIngressUnavailable = ingress.ErrArtifactIngressUnavailable

func validateMultipartOperationBindings(codec MultipartOperationIngress, snapshot *operations.Snapshot) error {
	return ingress.ValidateMultipartOperationBindings(codec, snapshot)
}

func validateOperationIngress(codec OperationIngress) error {
	return ingress.ValidateOperationIngress(codec)
}
