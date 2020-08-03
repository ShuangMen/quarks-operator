package manifest

import (
	"context"

	"code.cloudfoundry.org/quarks-operator/pkg/kube/util/simpledns"
	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// DomainNameService abstraction.
type DomainNameService interface {
	// DNSSetting get the DNS settings for POD.
	DNSSetting(namespace string) (corev1.DNSPolicy, *corev1.PodDNSConfig, error)

	// Apply a DNS server to the given namespace, if required.
	Apply(ctx context.Context, namespace string, c kubernetes.Interface) error
}

// NewDNS returns the DNS service management struct
func NewDNS(m Manifest) (DomainNameService, error) {
	for _, addon := range m.AddOns {
		if addon.Name == BoshDNSAddOnName {
			var err error
			dns, err := NewBoshDomainNameService(addon, m.InstanceGroups)
			if err != nil {
				return nil, errors.Wrapf(err, "error loading BOSH DNS configuration")
			}
			return dns, nil
		}
	}

	return simpledns.NewSimpleDomainNameService(), nil
}

// Validate that all job properties of the addon section can be decoded
func Validate(m Manifest) error {
	_, err := NewDNS(m)
	return err
}
