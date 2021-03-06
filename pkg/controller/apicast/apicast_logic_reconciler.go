package apicast

import (
	"context"
	"net/url"
	"reflect"

	apicast "github.com/3scale/apicast-operator/pkg/apicast"
	appscommon "github.com/3scale/apicast-operator/pkg/apis/apps"
	appsv1alpha1 "github.com/3scale/apicast-operator/pkg/apis/apps/v1alpha1"

	"github.com/3scale/apicast-operator/pkg/k8sutils"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	AdmPortalSecretResverAnnotation            = "apicast.apps.3scale.net/admin-portal-secret-resource-version"
	GatewayConfigurationSecretResverAnnotation = "apicast.apps.3scale.net/gateway-configuration-secret-resource-version"
)

type APIcastLogicReconciler struct {
	BaseReconciler
	APIcastCR *appsv1alpha1.APIcast
}

type apicastUserProvidedSecrets struct {
	adminPortalCredentialsSecret *v1.Secret
	gatewayEmbeddedConfigSecret  *v1.Secret
}

func NewAPIcastLogicReconciler(b BaseReconciler, cr *appsv1alpha1.APIcast) APIcastLogicReconciler {
	return APIcastLogicReconciler{
		BaseReconciler: b,
		APIcastCR:      cr,
	}
}

func (r *APIcastLogicReconciler) namespacedNameOnCR(obj metav1.Object) types.NamespacedName {
	return types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: r.APIcastCR.Namespace,
	}
}

func (r *APIcastLogicReconciler) Reconcile() (reconcile.Result, error) {
	r.Logger().WithValues("Name", r.APIcastCR.Name, "Namespace", r.APIcastCR.Namespace)

	appliedInitialization, err := r.initialize()
	if err != nil {
		return reconcile.Result{}, err
	}
	if appliedInitialization {
		// Stop the reconciliation cycle and order requeue to stop processing
		// of reconciliation
		return reconcile.Result{Requeue: true}, nil
	}

	adminPortalCredentialsSecret, changed, err := r.reconcileAdminPortalCredentials()
	if err != nil {
		return reconcile.Result{}, err
	}
	if changed {
		return reconcile.Result{Requeue: true}, nil
	}

	gatewayEmbeddedConfigSecret, changed, err := r.reconcileGatewayEmbbededConfig()
	if err != nil {
		return reconcile.Result{}, err
	}
	if changed {
		return reconcile.Result{Requeue: true}, nil
	}

	userProvidedSecrets := &apicastUserProvidedSecrets{
		adminPortalCredentialsSecret: adminPortalCredentialsSecret,
		gatewayEmbeddedConfigSecret:  gatewayEmbeddedConfigSecret,
	}

	// TODO this function does a little bit of creating the desiredApicast and
	// also validation. Also validation should be done BEFORE
	// the initialization probably
	desiredAPIcast, err := r.internalAPIcast(userProvidedSecrets)
	if err != nil {
		return reconcile.Result{}, err
	}

	err = r.reconcileDeployment(*desiredAPIcast.Deployment())
	if err != nil {
		return reconcile.Result{}, err
	}

	err = r.reconcileService(*desiredAPIcast.Service())
	if err != nil {
		return reconcile.Result{}, err
	}

	if r.APIcastCR.Spec.ExposedHost != nil {
		err = r.reconcileIngress(*desiredAPIcast.Ingress())
		if err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *APIcastLogicReconciler) getAdminPortalCredentialsSecret() (*v1.Secret, error) {
	adminPortalSecretReference := r.APIcastCR.Spec.AdminPortalCredentialsRef
	adminPortalNamespace := r.APIcastCR.Namespace

	if adminPortalSecretReference.Name == "" {
		return nil, fmt.Errorf("Field 'Name' not specified for AdminPortalCredentialsRef Secret Reference")
	}

	adminPortalCredentialsNamespacedName := types.NamespacedName{
		Name:      adminPortalSecretReference.Name,
		Namespace: adminPortalNamespace,
	}

	adminPortalCredentialsSecret := v1.Secret{}
	err := r.Client().Get(context.TODO(), adminPortalCredentialsNamespacedName, &adminPortalCredentialsSecret)

	if err != nil {
		return nil, err
	}

	secretStringData := k8sutils.SecretStringDataFromData(adminPortalCredentialsSecret)
	adminPortalURL, ok := secretStringData[apicast.AdminPortalURLAttributeName]
	if !ok {
		return nil, fmt.Errorf("Required key '%s' not found in secret '%s'", apicast.AdminPortalURLAttributeName, adminPortalCredentialsSecret.Name)
	}

	parsedURL, err := url.Parse(adminPortalURL)
	if err != nil {
		return nil, err
	}

	accessToken := parsedURL.User.Username()
	if accessToken == "" {
		return nil, fmt.Errorf("Access Token required in %s URL", apicast.AdminPortalURLAttributeName)
	}

	return &adminPortalCredentialsSecret, err
}

func (r *APIcastLogicReconciler) reconcileAdminPortalCredentials() (*v1.Secret, bool, error) {
	if r.APIcastCR.Spec.AdminPortalCredentialsRef == nil {
		return nil, false, nil
	}

	adminPortalCredentialsSecret, err := r.getAdminPortalCredentialsSecret()
	if err != nil {
		return nil, false, err
	}

	changed, err := r.ensureOwnerReference(adminPortalCredentialsSecret)
	if err != nil {
		return nil, changed, err
	}

	if changed {
		r.Logger().Info(fmt.Sprintf("Updating %s", k8sutils.ObjectInfo(adminPortalCredentialsSecret)))
		err = r.Client().Update(context.TODO(), adminPortalCredentialsSecret)
		if err != nil {
			return nil, changed, err
		}
	}

	return adminPortalCredentialsSecret, changed, nil
}

func (r *APIcastLogicReconciler) reconcileGatewayEmbbededConfig() (*v1.Secret, bool, error) {
	if r.APIcastCR.Spec.EmbeddedConfigurationSecretRef == nil {
		return nil, false, nil
	}

	gatewayEmbeddedConfigSecret, err := r.getGatewayEmbeddedConfigSecret()
	if err != nil {
		return nil, false, err
	}

	changed, err := r.ensureOwnerReference(gatewayEmbeddedConfigSecret)
	if err != nil {
		return nil, changed, err
	}

	if changed {
		r.Logger().Info(fmt.Sprintf("Updating %s", k8sutils.ObjectInfo(gatewayEmbeddedConfigSecret)))
		err = r.Client().Update(context.TODO(), gatewayEmbeddedConfigSecret)
		if err != nil {
			return nil, changed, err
		}
	}

	return gatewayEmbeddedConfigSecret, changed, nil
}

func (r *APIcastLogicReconciler) getGatewayEmbeddedConfigSecret() (*v1.Secret, error) {
	gatewayConfigSecretReference := r.APIcastCR.Spec.EmbeddedConfigurationSecretRef
	gatewayConfigSecretNamespace := r.APIcastCR.Namespace

	if gatewayConfigSecretReference.Name == "" {
		return nil, fmt.Errorf("Field 'Name' not specified for EmbeddedConfigurationSecretRef Secret Reference")
	}

	gatewayConfigSecretNamespacedName := types.NamespacedName{
		Name:      gatewayConfigSecretReference.Name,
		Namespace: gatewayConfigSecretNamespace,
	}

	gatewayConfigSecret := v1.Secret{}
	err := r.Client().Get(context.TODO(), gatewayConfigSecretNamespacedName, &gatewayConfigSecret)

	if err != nil {
		return nil, err
	}

	secretStringData := k8sutils.SecretStringDataFromData(gatewayConfigSecret)
	if _, ok := secretStringData[apicast.EmbeddedConfigurationSecretKey]; !ok {
		return nil, fmt.Errorf("Required key '%s' not found in secret '%s'", apicast.EmbeddedConfigurationSecretKey, gatewayConfigSecret.Name)
	}

	return &gatewayConfigSecret, err
}

func (r APIcastLogicReconciler) ensureOwnerReference(obj metav1.Object) (bool, error) {
	changed := false

	originalSize := len(obj.GetOwnerReferences())
	err := r.setOwnerReference(obj)
	if err != nil {
		return false, err
	}

	newSize := len(obj.GetOwnerReferences())
	if originalSize != newSize {
		changed = true
	}

	return changed, nil
}

func (r *APIcastLogicReconciler) UserProvidedSecretResourceVersionAnnotations(userProvidedSecrets *apicastUserProvidedSecrets) map[string]string {
	annotations := map[string]string{}

	if userProvidedSecrets.adminPortalCredentialsSecret != nil {
		annotations[AdmPortalSecretResverAnnotation] = userProvidedSecrets.adminPortalCredentialsSecret.ResourceVersion
	}

	if userProvidedSecrets.gatewayEmbeddedConfigSecret != nil {
		annotations[GatewayConfigurationSecretResverAnnotation] = userProvidedSecrets.gatewayEmbeddedConfigSecret.ResourceVersion
	}

	return annotations
}

// APIcastFromCRContents returns an apicast.APIcast instance. This method has
// been implemented in order to be able to obtain an APIcast instance from
// outside the reconciler code and avoid executing the Reconcile method. Notice
// how we get the user provided secrets (and just get, not reconcile
// them like we do in the Reconcile method)
func (r *APIcastLogicReconciler) APIcastFromCRContents() (*apicast.APIcast, error) {
	var adminPortalCredentialsSecret *v1.Secret
	var gatewayEmbeddedConfigSecret *v1.Secret
	var err error

	if r.APIcastCR.Spec.EmbeddedConfigurationSecretRef != nil {
		gatewayEmbeddedConfigSecret, err = r.getGatewayEmbeddedConfigSecret()
		if err != nil {
			return nil, err
		}
	}

	if r.APIcastCR.Spec.AdminPortalCredentialsRef != nil {
		adminPortalCredentialsSecret, err = r.getAdminPortalCredentialsSecret()
		if err != nil {
			return nil, err
		}
	}

	userProvidedSecrets := &apicastUserProvidedSecrets{
		adminPortalCredentialsSecret: adminPortalCredentialsSecret,
		gatewayEmbeddedConfigSecret:  gatewayEmbeddedConfigSecret,
	}

	apicast, err := r.internalAPIcast(userProvidedSecrets)
	if err != nil {
		return nil, err
	}

	return &apicast, nil
}

func (r *APIcastLogicReconciler) internalAPIcast(userProvidedSecrets *apicastUserProvidedSecrets) (apicast.APIcast, error) {
	var err error

	apicastFullName := "apicast-" + r.APIcastCR.Name
	apicastExposedHost := apicast.ExposedHost{}
	if r.APIcastCR.Spec.ExposedHost != nil {
		apicastExposedHost.Host = r.APIcastCR.Spec.ExposedHost.Host
		apicastExposedHost.TLS = r.APIcastCR.Spec.ExposedHost.TLS
	}
	apicastOwnerRef := asOwner(r.APIcastCR)

	var deploymentEnvironment *string
	if r.APIcastCR.Spec.DeploymentEnvironment != nil {
		res := string(*r.APIcastCR.Spec.DeploymentEnvironment)
		deploymentEnvironment = &res
	}

	deploymentAnnotations := r.UserProvidedSecretResourceVersionAnnotations(userProvidedSecrets)

	var adminPortalSecretName *string
	if userProvidedSecrets.adminPortalCredentialsSecret != nil {
		tmpAdminPortalSecretName := userProvidedSecrets.adminPortalCredentialsSecret.Name
		adminPortalSecretName = &tmpAdminPortalSecretName
	}

	var gatewayConfigurationSecretName *string
	if userProvidedSecrets.gatewayEmbeddedConfigSecret != nil {
		tmpGatewayConfigurationSecretName := userProvidedSecrets.gatewayEmbeddedConfigSecret.Name
		gatewayConfigurationSecretName = &tmpGatewayConfigurationSecretName
	}

	image := apicast.GetDefaultImageVersion()
	if r.APIcastCR.Spec.Image != nil {
		image = *r.APIcastCR.Spec.Image
	}

	serviceAccount := "default"
	if r.APIcastCR.Spec.ServiceAccount != nil {
		serviceAccount = *r.APIcastCR.Spec.ServiceAccount
	}

	apicastResult := apicast.APIcast{
		DeploymentName:                   apicastFullName,
		ServiceName:                      apicastFullName,
		Replicas:                         int32(*r.APIcastCR.Spec.Replicas),
		AppLabel:                         "apicast",
		AdditionalAnnotations:            deploymentAnnotations,
		ServiceAccountName:               serviceAccount,
		Image:                            image,
		ExposedHost:                      apicastExposedHost,
		Namespace:                        r.APIcastCR.Namespace,
		OwnerReference:                   &apicastOwnerRef,
		AdminPortalCredentialsSecretName: adminPortalSecretName,
		DeploymentEnvironment:            deploymentEnvironment,
		DNSResolverAddress:               r.APIcastCR.Spec.DNSResolverAddress,
		EnabledServices:                  r.APIcastCR.Spec.EnabledServices,
		ConfigurationLoadMode:            r.APIcastCR.Spec.ConfigurationLoadMode,
		LogLevel:                         r.APIcastCR.Spec.LogLevel,
		PathRoutingEnabled:               r.APIcastCR.Spec.PathRoutingEnabled,
		ResponseCodesIncluded:            r.APIcastCR.Spec.ResponseCodesIncluded,
		CacheConfigurationSeconds:        r.APIcastCR.Spec.CacheConfigurationSeconds,
		ManagementAPIScope:               r.APIcastCR.Spec.ManagementAPIScope,
		OpenSSLPeerVerificationEnabled:   r.APIcastCR.Spec.OpenSSLPeerVerificationEnabled,
		GatewayConfigurationSecretName:   gatewayConfigurationSecretName,
	}

	return apicastResult, err
}

func (r *APIcastLogicReconciler) namespacedName(object metav1.Object) types.NamespacedName {
	return types.NamespacedName{
		Name:      object.GetName(),
		Namespace: object.GetNamespace(),
	}
}

func (r *APIcastLogicReconciler) initialize() (bool, error) {
	if appliedSomeInitialization := r.applyInitialization(); appliedSomeInitialization {
		r.Logger().Info(fmt.Sprintf("Updating %s", k8sutils.ObjectInfo(r.APIcastCR)))
		err := r.Client().Update(context.TODO(), r.APIcastCR)
		if err != nil {
			return false, err
		}
		r.Logger().Info("APIcast resource missed optional fields. Updated CR which triggered a new reconciliation event")
		return true, nil
	}
	return false, nil
}

func (r *APIcastLogicReconciler) applyInitialization() bool {
	var defaultAPIcastReplicas int64 = 1
	appliedInitialization := false

	if r.APIcastCR.Spec.Replicas == nil {
		r.APIcastCR.Spec.Replicas = &defaultAPIcastReplicas
		appliedInitialization = true
	}

	return appliedInitialization
}

// asOwner returns an owner reference set as the tenant CR
func asOwner(a *appsv1alpha1.APIcast) metav1.OwnerReference {
	trueVar := true
	return metav1.OwnerReference{
		APIVersion: appsv1alpha1.SchemeGroupVersion.String(),
		Kind:       appscommon.APIcastKind,
		Name:       a.Name,
		UID:        a.UID,
		Controller: &trueVar,
	}
}

func (r *APIcastLogicReconciler) setOwnerReference(obj metav1.Object) error {
	ro, ok := obj.(runtime.Object)
	if !ok {
		return fmt.Errorf("is not a %T a runtime.Object, cannot call setOwnerReference", obj)
	}
	err := controllerutil.SetControllerReference(r.APIcastCR, obj, r.Scheme())

	if err != nil {
		r.Logger().Error(err, "Error setting OwnerReference on object. Requeuing request...",
			"Kind", ro.GetObjectKind(),
			"Namespace", obj.GetNamespace(),
			"Name", obj.GetName(),
		)
	}
	return err
}

func (r *APIcastLogicReconciler) reconcileDeployment(desiredDeployment appsv1.Deployment) error {
	existingDeployment := appsv1.Deployment{}
	err := r.Client().Get(context.TODO(), r.namespacedName(&desiredDeployment), &existingDeployment)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Logger().Info(fmt.Sprintf("Creating %s", k8sutils.ObjectInfo(&desiredDeployment)))
			err = r.Client().Create(context.TODO(), &desiredDeployment)
			return err
		}
		return err
	}

	changed := false

	if existingDeployment.Spec.Replicas != desiredDeployment.Spec.Replicas {
		existingDeployment.Spec.Replicas = desiredDeployment.Spec.Replicas
		changed = true
	}
	if existingDeployment.Spec.Template.Spec.Containers[0].Image != desiredDeployment.Spec.Template.Spec.Containers[0].Image {
		existingDeployment.Spec.Template.Spec.Containers[0].Image = desiredDeployment.Spec.Template.Spec.Containers[0].Image
		changed = true

	}
	if existingDeployment.Spec.Template.Spec.ServiceAccountName != desiredDeployment.Spec.Template.Spec.ServiceAccountName {
		changed = true
		existingDeployment.Spec.Template.Spec.ServiceAccountName = desiredDeployment.Spec.Template.Spec.ServiceAccountName
	}

	updatedTmp := ReconcileEnvVar(&existingDeployment.Spec.Template.Spec.Containers[0].Env, desiredDeployment.Spec.Template.Spec.Containers[0].Env)
	changed = changed || updatedTmp

	// They are annotations of the PodTemplate, part of the Spec, not part of the meta info of the Pod or Environment object itself
	// It is not expected any controller to update them, so we use "set" approach, instead of merge.
	// This way any removed annotation from desired (due to change in CR) will be removed in existing too.
	if !reflect.DeepEqual(existingDeployment.Spec.Template.Annotations, desiredDeployment.Spec.Template.Annotations) {
		changed = true
		existingDeployment.Spec.Template.Annotations = desiredDeployment.Spec.Template.Annotations
	}

	if !reflect.DeepEqual(existingDeployment.Spec.Template.Spec.Volumes, desiredDeployment.Spec.Template.Spec.Volumes) {
		changed = true
		existingDeployment.Spec.Template.Spec.Volumes = desiredDeployment.Spec.Template.Spec.Volumes
	}

	if !reflect.DeepEqual(existingDeployment.Spec.Template.Spec.Containers[0].VolumeMounts, desiredDeployment.Spec.Template.Spec.Containers[0].VolumeMounts) {
		changed = true
		existingDeployment.Spec.Template.Spec.Containers[0].VolumeMounts = desiredDeployment.Spec.Template.Spec.Containers[0].VolumeMounts
	}

	if changed {
		r.Logger().Info(fmt.Sprintf("Updating %s", k8sutils.ObjectInfo(&existingDeployment)))
		err = r.Client().Update(context.TODO(), &existingDeployment)
		return err
	}

	return nil
}

func (r *APIcastLogicReconciler) reconcileService(desiredService v1.Service) error {
	existingService := v1.Service{}
	err := r.Client().Get(context.TODO(), r.namespacedName(&desiredService), &existingService)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Logger().Info(fmt.Sprintf("Creating %s", k8sutils.ObjectInfo(&desiredService)))
			err = r.Client().Create(context.TODO(), &desiredService)
		}
		return err
	}

	return err
}

func (r *APIcastLogicReconciler) reconcileIngress(desiredIngress extensions.Ingress) error {
	existingIngress := extensions.Ingress{}
	err := r.Client().Get(context.TODO(), r.namespacedName(&desiredIngress), &existingIngress)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Logger().Info(fmt.Sprintf("Creating %s", k8sutils.ObjectInfo(&desiredIngress)))
			err = r.Client().Create(context.TODO(), &desiredIngress)
		}
		return err
	}

	exposedHostIdx := -1
	for idx, rule := range existingIngress.Spec.Rules {
		if rule.Host == r.APIcastCR.Spec.ExposedHost.Host {
			exposedHostIdx = idx
		}
	}

	update := false

	if exposedHostIdx == -1 {
		existingIngress.Spec.Rules = desiredIngress.Spec.Rules
		update = true
	}

	if !reflect.DeepEqual(existingIngress.Spec.TLS, desiredIngress.Spec.TLS) {
		existingIngress.Spec.TLS = desiredIngress.Spec.TLS
		update = true
	}

	if update {
		r.Logger().Info(fmt.Sprintf("Updating %s", k8sutils.ObjectInfo(&existingIngress)))
		err = r.Client().Update(context.TODO(), &existingIngress)
		if err != nil {
			return err
		}
	}

	return nil
}
