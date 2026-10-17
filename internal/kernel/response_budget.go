package kernel

import (
	"fmt"
	"io"
	"sync"
)

type responseBodyLease struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *responseBodyLease) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

type responseEventBudget struct{ maxEvent, maxOutput, total int64 }

func (b *responseEventBudget) consume(event ResponseEvent) error {
	if b.maxEvent <= 0 && b.maxOutput <= 0 {
		return nil
	}
	eventBytes := int64(len(event.Raw)) + int64(len(event.Opaque)) + int64(len(event.Text)) + int64(len(event.Signature)) + int64(len(event.SignatureIssuer.ID)) + int64(len(event.ToolCallID)) + int64(len(event.ToolName)) + int64(len(event.ToolArguments)) + int64(len(event.Error)) + int64(len(event.ResponseID)) + int64(len(event.ItemID)) + int64(len(event.ContentType)) + int64(len(event.BlockType)) + int64(len(event.StopReason))
	var bodyBytes int64
	for _, artifact := range event.Artifacts {
		eventBytes += int64(len(artifact.TypeRef.ID)+len(artifact.Role)+len(artifact.MediaType)+len(artifact.Sensitivity)+len(artifact.Value)) + 64
		if artifact.Body != nil && artifact.SizeBytes > 0 && b.maxOutput > 0 {
			if artifact.SizeBytes > b.maxOutput-bodyBytes {
				return fmt.Errorf("decoded response exceeds %d-byte operation output limit", b.maxOutput)
			}
			bodyBytes += artifact.SizeBytes
		}
	}
	if b.maxEvent > 0 && eventBytes > b.maxEvent {
		return fmt.Errorf("response event %q exceeds %d-byte operation buffer limit", event.Kind, b.maxEvent)
	}
	if b.maxOutput > 0 {
		remaining := b.maxOutput - b.total
		if eventBytes > remaining || bodyBytes > remaining-eventBytes {
			return fmt.Errorf("decoded response exceeds %d-byte operation output limit", b.maxOutput)
		}
		b.total += eventBytes + bodyBytes
	}
	return nil
}
