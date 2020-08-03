package simpledns

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// SimpleDomainNameService emulates old behaviour without BOSH DNS.
type SimpleDomainNameService struct {
}

// NewSimpleDomainNameService creates a new SimpleDomainNameService.
func NewSimpleDomainNameService() *SimpleDomainNameService {
	return &SimpleDomainNameService{}
}

// DNSSetting see interface.
func (dns *SimpleDomainNameService) DNSSetting(_ string) (corev1.DNSPolicy, *corev1.PodDNSConfig, error) {
	return corev1.DNSClusterFirst, nil, nil
}

// Apply is not required for the simple domain service.
func (dns *SimpleDomainNameService) Apply(ctx context.Context, namespace string, c kubernetes.Interface) error {
	return nil
}
