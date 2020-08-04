package boshdeployment

import (
	"context"
	"time"

	"github.com/SUSE/go-patch/patch"
	boshtpl "github.com/cloudfoundry/bosh-cli/director/template"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bdm "code.cloudfoundry.org/quarks-operator/pkg/bosh/manifest"
	bdv1 "code.cloudfoundry.org/quarks-operator/pkg/kube/apis/boshdeployment/v1alpha1"
	boshnames "code.cloudfoundry.org/quarks-operator/pkg/kube/util/names"
	mutateqs "code.cloudfoundry.org/quarks-secret/pkg/kube/util/mutate"
	"code.cloudfoundry.org/quarks-utils/pkg/config"
	log "code.cloudfoundry.org/quarks-utils/pkg/ctxlog"
	"code.cloudfoundry.org/quarks-utils/pkg/meltdown"
	"code.cloudfoundry.org/quarks-utils/pkg/versionedsecretstore"
)

// NewWithOpsReconciler returns a new reconcile.Reconciler
func NewWithOpsReconciler(ctx context.Context, config *config.Config, mgr manager.Manager, srf setReferenceFunc) reconcile.Reconciler {
	return &ReconcileWithOps{
		ctx:                  ctx,
		config:               config,
		client:               mgr.GetClient(),
		scheme:               mgr.GetScheme(),
		setReference:         srf,
		versionedSecretStore: versionedsecretstore.NewVersionedSecretStore(mgr.GetClient()),
	}
}

// ReconcileWithOps reconciles the with ops manifest secret
type ReconcileWithOps struct {
	ctx                  context.Context
	config               *config.Config
	client               client.Client
	scheme               *runtime.Scheme
	resolver             DesiredManifest
	setReference         setReferenceFunc
	versionedSecretStore versionedsecretstore.VersionedSecretStore
}

// Reconcile reconciles an withOps secret and generates the corresponding
// desired manifest secret.
func (r *ReconcileWithOps) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// Set the ctx to be Background, as the top-level context for incoming requests.
	ctx, cancel := context.WithTimeout(r.ctx, r.config.CtxTimeOut)
	defer cancel()

	log.Infof(ctx, "Reconciling withOps secret '%s'", request.NamespacedName)
	withOpsSecret := &corev1.Secret{}
	err := r.client.Get(ctx, request.NamespacedName, withOpsSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Debug(ctx, "Skip reconcile: WithOps secret not found")
			return reconcile.Result{}, nil
		}

		// Error reading the object - requeue the request.
		log.WithEvent(withOpsSecret, "GetWithOpsSecret").Errorf(ctx, "Failed to get withOps secret '%s': %v", request.NamespacedName, err)
		return reconcile.Result{RequeueAfter: time.Second * 5}, nil
	}

	// Print belwo and heck - day done
	annotations := withOpsSecret.GetAnnotations()
	labels := withOpsSecret.GetLabels()
	boshdeploymentName := labels[bdv1.LabelDeploymentName]

	lastReconcile, ok := annotations[meltdown.AnnotationLastReconcile]
	if (ok && lastReconcile == "") || !ok {
		withOpsSecret.Annotations[meltdown.AnnotationLastReconcile] = metav1.Now().Format(time.RFC3339)
		err = r.client.Update(ctx, withOpsSecret)
		if err != nil {
			return reconcile.Result{},
				log.WithEvent(withOpsSecret, "UpdateError").Errorf(ctx, "failed to update lastreconcile annotation on withops secret for bdpl '%s': %v", boshdeploymentName, err)
		}
		log.Infof(ctx, "Meltdown started for '%s'", request.NamespacedName)

		return reconcile.Result{RequeueAfter: ReconcileSkipDuration}, nil
	}

	if meltdown.NewAnnotationWindow(ReconcileSkipDuration, annotations).Contains(time.Now()) {
		log.Infof(ctx, "Meltdown in progress for '%s'", request.NamespacedName)
		return reconcile.Result{}, nil
	}
	log.Infof(ctx, "Meltdown ended for '%s'", request.NamespacedName)

	/*withOpsSecret.Annotations[meltdown.AnnotationLastReconcile] = ""
	err = r.client.Update(ctx, withOpsSecret)
	if err != nil {
		return reconcile.Result{},
			log.WithEvent(withOpsSecret, "UpdateError").Errorf(ctx, "failed to update lastreconcile annotation on withops secret for bdpl '%s': %v", boshdeploymentName, err)
	}

	//_ = withOpsSecret.Data["manifest.yaml"]

	/*log.Debug(ctx, "Interpolate variables")
	err = r.interpolateVariables(ctx, manifestData, request.Namespace)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 5},
			log.WithEvent(withOpsSecret, "WithOpsManifestError").Errorf(ctx, "failed to interpolated variables for BOSHDeployment '%s': %v", request.NamespacedName, err)
	}

	/*log.Debug(ctx, "Creating desired manifest secret")
	manifestSecret, err := r.createDesiredManifest(ctx, bdpl, manifestData)
	if err != nil {
		return reconcile.Result{},
			log.WithEvent(withOpsSecret, "WithOpsManifestError").Errorf(ctx, "failed to create with-ops manifest secret for BOSHDeployment '%s': %v", request.NamespacedName, err)
	}*/

	return reconcile.Result{}, nil
}

// createDesiredManifest creates a secret containing the deployment manifest with ops files applied and variables interpolated
func (r *ReconcileWithOps) createDesiredManifest(ctx context.Context, bdpl *bdv1.BOSHDeployment, manifest bdm.Manifest) (*corev1.Secret, error) {
	log.Debug(ctx, "Creating desired manifest")

	manifestBytes, err := manifest.Marshal()
	if err != nil {
		return nil, log.WithEvent(bdpl, "ManifestWithOpsMarshalError").Errorf(ctx, "Error marshaling the manifest '%s': %s", bdpl.GetNamespacedName(), err)
	}

	manifestSecretName := bdv1.DeploymentSecretTypeDesiredManifest.String()

	// Create a secret object for the manifest
	manifestSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      manifestSecretName,
			Namespace: bdpl.GetNamespace(),
			Labels: map[string]string{
				bdv1.LabelDeploymentName:       bdpl.Name,
				bdv1.LabelDeploymentSecretType: bdv1.DeploymentSecretTypeManifestWithOps.String(),
			},
		},
		StringData: map[string]string{
			"manifest.yaml": string(manifestBytes),
		},
	}

	// Set ownership reference
	if err := r.setReference(bdpl, manifestSecret, r.scheme); err != nil {
		return nil, log.WithEvent(bdpl, "ManifestWithOpsRefError").Errorf(ctx, "failed to set ownerReference for Secret '%s/%s': %v", bdpl.Namespace, manifestSecretName, err)
	}

	// Apply the secret
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, manifestSecret, mutateqs.SecretMutateFn(manifestSecret))
	if err != nil {
		return nil, log.WithEvent(bdpl, "ManifestWithOpsApplyError").Errorf(ctx, "failed to apply Secret '%s/%s': %v", bdpl.Namespace, manifestSecretName, err)
	}

	log.Debugf(ctx, "ResourceReference secret '%s/%s' has been %s", bdpl.Namespace, manifestSecret.Name, op)

	return manifestSecret, nil
}

// interpolateVariables reads explicit secrets and writes an interpolated manifest into desired manifest secret.
func (r *ReconcileWithOps) interpolateVariables(ctx context.Context, withOpsManifestData []byte, namespace string) error {
	var vars []boshtpl.Variables

	withOpsManifest, err := bdm.LoadYAML(withOpsManifestData)
	if err != nil {
		return err
	}
	for _, variable := range withOpsManifest.Variables {
		staticVars := boshtpl.StaticVariables{}

		varName := variable.Name
		varSecretName := boshnames.SecretVariableName(varName)

		log.Infof(ctx, "Fetching secret '%s' for interpolation", varSecretName)
		varSecret := &corev1.Secret{}
		err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: varSecretName}, varSecret)
		if err != nil {
			return err
		}
		// loop over and make map[string]string
		varSecretData := varSecret.Data
		log.Info(ctx, "========================")
		log.Info(ctx, varSecretData)

		vars = append(vars, staticVars)
	}

	_, err = InterpolateExplicitVariables(withOpsManifestData, vars, true)
	if err != nil {
		return errors.Wrap(err, "failed to interpolate explicit variables")
	}
	return nil

	/*for _, variable := range variables {
		// Each directory is a variable name
		if variable.IsDir() {
			staticVars := boshtpl.StaticVariables{}
			// Each filename is a field name and its context is a variable value
			err = filepath.Walk(filepath.Clean(variablesDir+"/"+variable.Name()), func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}

				if !info.IsDir() {
					_, varFileName := filepath.Split(path)
					// Skip the symlink to a directory
					if strings.HasPrefix(varFileName, "..") {
						return filepath.SkipDir
					}
					varBytes, err := ioutil.ReadFile(path)
					if err != nil {
						return errors.Wrapf(err, "could not read variables variable %s", variable.Name())
					}

					// If variable type is password, set password value directly
					switch varFileName {
					case "password":
						staticVars[variable.Name()] = string(varBytes)
					default:
						staticVars[variable.Name()] = MergeStaticVar(staticVars[variable.Name()], varFileName, string(varBytes))
					}
				}
				return nil
			})
			if err != nil {
				return errors.Wrapf(err, "could not read directory  %s", variable.Name())
			}

			vars = append(vars, staticVars)
		}
	}

	yamlBytes, err = InterpolateExplicitVariables(boshManifestBytes, vars, true)
	if err != nil {
		return errors.Wrap(err, "failed to interpolate explicit variables")
	}

	jsonBytes, err := json.Marshal(map[string]string{
		DesiredManifestKeyName: string(yamlBytes),
	})
	if err != nil {
		return errors.Wrapf(err, "could not marshal json output")
	}

	err = ioutil.WriteFile(outputFilePath, jsonBytes, 0644)
	if err != nil {
		return err
	}

	return nil*/
}

// InterpolateExplicitVariables interpolates explicit variables in the manifest
// Expects an array of maps, each element being a variable: [{ "name":"foo", "password": "value" }, {"name": "bar", "ca": "---"} ]
// Returns the new manifest as a byte array
func InterpolateExplicitVariables(boshManifestBytes []byte, vars []boshtpl.Variables, expectAllKeys bool) ([]byte, error) {
	multiVars := boshtpl.NewMultiVars(vars)
	tpl := boshtpl.NewTemplate(boshManifestBytes)

	// Following options are empty for cf-operator
	op := patch.Ops{}
	evalOpts := boshtpl.EvaluateOpts{
		ExpectAllKeys:     expectAllKeys,
		ExpectAllVarsUsed: false,
	}

	yamlBytes, err := tpl.Evaluate(multiVars, op, evalOpts)
	if err != nil {
		return nil, errors.Wrapf(err, "could not evaluate variables")
	}

	m, err := bdm.LoadYAML(yamlBytes)
	if err != nil {
		return nil, errors.Wrapf(err, "could not evaluate variables")
	}

	yamlBytes, err = m.Marshal()
	if err != nil {
		return nil, errors.Wrapf(err, "could not evaluate variables")
	}

	return yamlBytes, nil
}

// MergeStaticVar builds a map of values used for BOSH explicit variable interpolation
func MergeStaticVar(staticVar interface{}, field string, value string) interface{} {
	if staticVar == nil {
		staticVar = map[interface{}]interface{}{
			field: value,
		}
	} else {
		staticVarMap := staticVar.(map[interface{}]interface{})
		staticVarMap[field] = value
		staticVar = staticVarMap
	}

	return staticVar
}
