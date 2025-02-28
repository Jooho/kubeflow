package e2e

import (
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	nbv1 "github.com/kubeflow/kubeflow/components/notebook-controller/api/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

func creationTestSuite(t *testing.T) {
	testCtx, err := NewTestContext()
	require.NoError(t, err)
	for _, nbContext := range testCtx.testNotebooks {
		// prepend Notebook name to every subtest
		t.Run(nbContext.nbObjectMeta.Name, func(t *testing.T) {
			t.Run("Creation of Notebook instance", func(t *testing.T) {
				err = testCtx.testNotebookCreation(nbContext)
				require.NoError(t, err, "error creating Notebook object ")
			})
			t.Run("Notebook Route Validation", func(t *testing.T) {
				err = testCtx.testNotebookRouteCreation(nbContext.nbObjectMeta)
				require.NoError(t, err, "error testing Route for Notebook ")
			})
			t.Run("Notebook Statefulset Validation", func(t *testing.T) {
				err = testCtx.testNotebookValidation(nbContext.nbObjectMeta)
				require.NoError(t, err, "error testing StatefulSet for Notebook ")
			})
			t.Run("Notebook OAuth sidecar Validation", func(t *testing.T) {
				err = testCtx.testNotebookOAuthSidecar(nbContext.nbObjectMeta)
				require.NoError(t, err, "error testing sidecar for Notebook ")
			})
			t.Run("Verify Notebook Traffic", func(t *testing.T) {
				err = testCtx.testNotebookTraffic(nbContext.nbObjectMeta)
				require.NoError(t, err, "error testing Notebook traffic ")
			})
			t.Run("Verify Notebook Culling", func(t *testing.T) {
				err = testCtx.testNotebookCulling(nbContext.nbObjectMeta)
				require.NoError(t, err, "error testing Notebook culling ")
			})
		})
	}

}

func (tc *testContext) testNotebookCreation(nbContext notebookContext) error {

	testNotebook := &nbv1.Notebook{
		ObjectMeta: *nbContext.nbObjectMeta,
		Spec:       *nbContext.nbSpec,
	}

	// Create test Notebook resource if not already created
	notebookLookupKey := types.NamespacedName{Name: testNotebook.Name, Namespace: testNotebook.Namespace}
	createdNotebook := nbv1.Notebook{}

	err := tc.customClient.Get(tc.ctx, notebookLookupKey, &createdNotebook)
	if err != nil {
		if errors.IsNotFound(err) {
			nberr := wait.Poll(tc.resourceRetryInterval, tc.resourceCreationTimeout, func() (done bool, err error) {
				creationErr := tc.customClient.Create(tc.ctx, testNotebook)
				if creationErr != nil {
					log.Printf("Error creating Notebook resource %v: %v, trying again",
						testNotebook.Name, creationErr)
					return false, nil
				} else {
					return true, nil
				}
			})
			if nberr != nil {
				return fmt.Errorf("error creating test Notebook %s: %v", testNotebook.Name, nberr)
			}
		} else {
			return fmt.Errorf("error getting test Notebook %s: %v", testNotebook.Name, err)
		}
	}
	return nil
}

func (tc *testContext) testNotebookRouteCreation(nbMeta *metav1.ObjectMeta) error {
	nbRoute, err := tc.getNotebookRoute(nbMeta)
	if err != nil {
		return fmt.Errorf("error getting Route for Notebook %v: %v", nbRoute.Name, err)
	}
	isReady := false
	for _, ingress := range nbRoute.Status.Ingress {
		if strings.Contains(ingress.Host, nbRoute.Name) {
			for _, condition := range ingress.Conditions {
				if condition.Type == routev1.RouteAdmitted && condition.Status == v1.ConditionTrue {
					isReady = true
				}
			}
		}
	}
	if !isReady {
		return fmt.Errorf("Notebook Route %s is not Ready.", nbRoute.Name)
	}
	return err
}

func (tc *testContext) testNotebookValidation(nbMeta *metav1.ObjectMeta) error {
	// Verify StatefulSet is running
	err := wait.Poll(tc.resourceRetryInterval, tc.resourceCreationTimeout, func() (done bool, err error) {
		notebookStatefulSet, err1 := tc.kubeClient.AppsV1().StatefulSets(tc.testNamespace).Get(tc.ctx,
			nbMeta.Name, metav1.GetOptions{})

		if err1 != nil {
			if errors.IsNotFound(err1) {
				return false, nil
			} else {
				log.Printf("Failed to get %s statefulset", nbMeta.Name)
				return false, err1
			}
		}
		if notebookStatefulSet.Status.AvailableReplicas == 1 &&
			notebookStatefulSet.Status.ReadyReplicas == 1 {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("error validating notebook StatefulSet: %s", err)
	}
	return nil
}

func (tc *testContext) testNotebookOAuthSidecar(nbMeta *metav1.ObjectMeta) error {

	nbPods, err := tc.kubeClient.CoreV1().Pods(tc.testNamespace).List(tc.ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("statefulset=%s", nbMeta.Name)})

	if err != nil {
		return fmt.Errorf("error retrieving Notebook pods :%v", err)
	}

	for _, nbpod := range nbPods.Items {
		if nbpod.Status.Phase != v1.PodRunning {
			return fmt.Errorf("notebook pod %v is not in Running phase", nbpod.Name)
		}
		for _, containerStatus := range nbpod.Status.ContainerStatuses {
			if containerStatus.Name == "oauth-proxy" {
				if !containerStatus.Ready {
					return fmt.Errorf("oauth-proxy container is not in Ready state for pod %v", nbpod.Name)
				}
			}
		}
	}
	return nil
}

func (tc *testContext) testNotebookTraffic(nbMeta *metav1.ObjectMeta) error {
	resp, err := tc.curlNotebookEndpoint(*nbMeta)
	if err != nil {
		return fmt.Errorf("error accessing Notebook Endpoint: %v ", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("Unexpected response from Notebook Endpoint")
	}
	return nil
}

func (tc *testContext) testNotebookCulling(nbMeta *metav1.ObjectMeta) error {
	// Create Configmap with culling configuration
	cullingConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notebook-controller-culler-config",
			Namespace: tc.testNamespace,
		},
		Data: map[string]string{
			"ENABLE_CULLING":        "true",
			"CULL_IDLE_TIME":        "2",
			"IDLENESS_CHECK_PERIOD": "1",
		},
	}

	_, err := tc.kubeClient.CoreV1().ConfigMaps(tc.testNamespace).Create(tc.ctx, cullingConfigMap,
		metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating configmapnotebook-controller-culler-config: %v", err)
	}
	// Restart the deployment to get changes from configmap
	controllerDeployment, err := tc.kubeClient.AppsV1().Deployments(tc.testNamespace).Get(tc.ctx,
		"notebook-controller-deployment", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting deployment %v: %v", controllerDeployment.Name, err)
	}

	defer tc.revertCullingConfiguration(cullingConfigMap.ObjectMeta, controllerDeployment.ObjectMeta)

	err = tc.rolloutDeployment(controllerDeployment.ObjectMeta)
	if err != nil {
		return fmt.Errorf("error rolling out the deployment with culling configuration: %v", err)
	}

	// Wait for server to shut down after 'CULL_IDLE_TIME' minutes(around 2.5 minutes)
	time.Sleep(150 * time.Second)
	// Verify that the notebook kernel has shutdown, and the notebook endpoint returns 503
	resp, err := tc.curlNotebookEndpoint(*nbMeta)
	if err != nil {
		return fmt.Errorf("error accessing Notebook Endpoint with 503: %v ", err)
	}
	if resp.StatusCode != 503 {
		return fmt.Errorf("Unexpected response from Notebook Endpoint")
	}
	return nil
}
