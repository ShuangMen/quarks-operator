package environment

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	gomegaConfig "github.com/onsi/ginkgo/config"
	"github.com/onsi/gomega"
	"github.com/pkg/errors"
	"github.com/spf13/afero"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc" //from https://github.com/kubernetes/client-go/issues/345
	"k8s.io/client-go/rest"

	"code.cloudfoundry.org/quarks-operator/pkg/kube/client/clientset/versioned"
	"code.cloudfoundry.org/quarks-operator/pkg/kube/operator"
	"code.cloudfoundry.org/quarks-operator/testing"
	qstsclient "code.cloudfoundry.org/quarks-statefulset/pkg/kube/client/clientset/versioned"
	"code.cloudfoundry.org/quarks-utils/pkg/config"
	utils "code.cloudfoundry.org/quarks-utils/testing/integration"
	"code.cloudfoundry.org/quarks-utils/testing/machine"
)

// Environment starts our operator and handles interaction with the k8s
// cluster used in the tests
type Environment struct {
	*utils.Environment
	Machine
	testing.Catalog
}

var (
	namespaceCounter int32
)

const (
	defaultTestMeltdownDuration     = 10
	defaultTestMeltdownRequeueAfter = 1
)

// NewEnvironment returns a new struct
func NewEnvironment(kubeConfig *rest.Config) *Environment {
	atomic.AddInt32(&namespaceCounter, 1)
	namespaceID := gomegaConfig.GinkgoConfig.ParallelNode*200 + int(namespaceCounter)
	// the single namespace used by this test
	ns := utils.GetNamespaceName(namespaceID)

	env := &Environment{
		Environment: &utils.Environment{
			ID:         namespaceID,
			Namespace:  ns,
			KubeConfig: kubeConfig,
			Config: &config.Config{
				CtxTimeOut:           10 * time.Second,
				MeltdownDuration:     defaultTestMeltdownDuration * time.Second,
				MeltdownRequeueAfter: defaultTestMeltdownRequeueAfter * time.Second,
				MonitoredID:          ns,
				OperatorNamespace:    ns,
				Fs:                   afero.NewOsFs(),
			},
		},
		Machine: Machine{
			Machine: machine.NewMachine(),
		},
	}
	gomega.SetDefaultEventuallyTimeout(env.PollTimeout)
	gomega.SetDefaultEventuallyPollingInterval(env.PollInterval)

	return env
}

// SetupClientsets initializes kube clientsets
func (e *Environment) SetupClientsets() error {
	var err error
	e.Clientset, err = kubernetes.NewForConfig(e.KubeConfig)
	if err != nil {
		return err
	}

	e.VersionedClientset, err = versioned.NewForConfig(e.KubeConfig)
	if err != nil {
		return err
	}
	e.QuarksStatefulSetClient, err = qstsclient.NewForConfig(e.KubeConfig)
	return err
}

// NodeIP returns a public IP of a node
func (e *Environment) NodeIP() (string, error) {
	if override, ok := os.LookupEnv("CF_OPERATOR_NODE_IP"); ok {
		// The user has specified a particular node IP to use; return that.
		return override, nil
	}

	nodes, err := e.Clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return "", errors.Wrap(err, "getting the list of nodes")
	}

	if len(nodes.Items) == 0 {
		return "", fmt.Errorf("got an empty list of nodes")
	}

	addresses := nodes.Items[0].Status.Addresses
	if len(addresses) == 0 {
		return "", fmt.Errorf("node has an empty list of addresses")
	}

	return addresses[0].Address, nil
}

const serviceAccountName = "quarks-operator-service-account"

// SetupServiceAccount creates a service account for the pod
func (e *Environment) SetupServiceAccount() error {
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: e.Namespace,
		},
	}

	client := e.Clientset.CoreV1().ServiceAccounts(e.Namespace)
	if _, err := client.Create(context.Background(), serviceAccount, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return errors.Wrapf(err, "could not create service account")
		}
	}

	// Bind the quarks-operator service account to the cluster-admin ClusterRole. Notice that the
	// RoleBinding is namespaced as opposed to ClusterRoleBinding which would give the service account
	// unrestricted permissions to any namespace.
	roleBinding := &v1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "variable-role",
			Namespace: e.Namespace,
		},
		Subjects: []v1.Subject{
			{
				Kind:      v1.ServiceAccountKind,
				Name:      serviceAccountName,
				Namespace: e.Namespace,
			},
		},
		RoleRef: v1.RoleRef{
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	rbac := e.Clientset.RbacV1().RoleBindings(e.Namespace)
	if _, err := rbac.Create(context.Background(), roleBinding, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return errors.Wrapf(err, "could not create role binding")
		}
	}
	return nil
}

// ApplyCRDs applies the CRDs to the cluster
func ApplyCRDs(kubeConfig *rest.Config) error {
	err := operator.ApplyCRDs(context.Background(), kubeConfig)
	if err != nil {
		return err
	}
	return nil
}
