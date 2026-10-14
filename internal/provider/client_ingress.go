package provider

import "github.com/fm39hz/gobroom/internal/ingress"

type ClientIngress = ingress.ClientIngress
type ClientIngressRoute = ingress.ClientIngressRoute
type JSONOperationIngress = ingress.JSONOperationIngress
type MultipartArtifactBinding = ingress.MultipartArtifactBinding
type MultipartOperationIngress = ingress.MultipartOperationIngress

func ValidateClientIngress(codec ClientIngress) error { return ingress.ValidateClientIngress(codec) }
