package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

var errQuotaLease = errors.New("quota credential lease rejected")

// fetchQuotaWithLease lends credential material only for one quota request.
// It rejects the entire result if the lease is no longer dispatchable.
func fetchQuotaWithLease(
	ctx context.Context,
	route kernel.Route,
	source provider.QuotaSource,
	endpoint kernel.Endpoint,
	transport kernel.Transport,
	acquire kernel.CredentialLeaseResolver,
	reportFailure func(stage, code string),
) ([]quota.Snapshot, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, errors.Join(errQuotaLease, err)
	}
	if acquire == nil {
		return nil, time.Time{}, errQuotaLease
	}
	lease, err := acquire(ctx, route)
	if lease != nil {
		defer lease.Release()
	}
	if err != nil {
		if reportFailure != nil {
			reportFailure("credential", "credential_error")
		}
		return nil, time.Time{}, errors.Join(errQuotaLease, err)
	}
	scope := kernel.CredentialLeaseScopeForQuota(route)
	if err := lease.ValidateForDispatch(ctx, scope); err != nil {
		if reportFailure != nil {
			reportFailure("lease", "unavailable")
		}
		return nil, time.Time{}, errors.Join(errQuotaLease, err)
	}
	credential, err := lease.Borrow(ctx, scope)
	if err != nil {
		if reportFailure != nil {
			reportFailure("lease", "unavailable")
		}
		return nil, time.Time{}, errors.Join(errQuotaLease, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, errors.Join(errQuotaLease, err)
	}
	if err := lease.ValidateForDispatch(ctx, scope); err != nil {
		if reportFailure != nil {
			reportFailure("lease", "unavailable")
		}
		return nil, time.Time{}, errors.Join(errQuotaLease, err)
	}
	requestedAt := time.Now()
	snapshots, fetchErr := source.Fetch(ctx, provider.QuotaRequest{
		Credential: credential, Route: route, Endpoint: endpoint, Transport: lease.GuardTransport(scope, transport),
		EndpointOptions: route.QuotaEndpointOptions, WindowName: route.QuotaWindowName,
		ReportFailure: func(reportErr error) {
			if reportFailure != nil {
				reportFailure("report", provider.QuotaFailureCode(reportErr))
			}
		},
	})
	if err := lease.ValidateForDispatch(ctx, scope); err != nil {
		if reportFailure != nil {
			reportFailure("lease", "unavailable")
		}
		return nil, requestedAt, errors.Join(errQuotaLease, err)
	}
	if fetchErr != nil {
		return nil, requestedAt, fetchErr
	}
	return snapshots, requestedAt, nil
}
