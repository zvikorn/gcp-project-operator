package projectclaim

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/openshift/cluster-api/pkg/util"
	gcpv1alpha1 "github.com/openshift/gcp-project-operator/pkg/apis/gcp/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CustomResourceAdapter struct {
	projectClaim     *gcpv1alpha1.ProjectClaim
	logger           logr.Logger
	client           client.Client
	projectReference *gcpv1alpha1.ProjectReference
}

type ObjectState bool

const (
	ObjectModified  ObjectState = true
	ObjectUnchanged ObjectState = false
)

const ProjectClaimFinalizer string = "finalizer.gcp.managed.openshift.io"

func NewCustomResourceAdapter(projectClaim *gcpv1alpha1.ProjectClaim, logger logr.Logger, client client.Client) *CustomResourceAdapter {
	projectReference := newMatchingProjectReference(projectClaim)
	return &CustomResourceAdapter{projectClaim, logger, client, projectReference}
}

func newMatchingProjectReference(projectClaim *gcpv1alpha1.ProjectClaim) *gcpv1alpha1.ProjectReference {

	return &gcpv1alpha1.ProjectReference{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projectClaim.GetNamespace() + "-" + projectClaim.GetName(),
			Namespace: gcpv1alpha1.ProjectReferenceNamespace,
		},
		Spec: gcpv1alpha1.ProjectReferenceSpec{
			GCPProjectID: "",
			ProjectClaimCRLink: gcpv1alpha1.NamespacedName{
				Name:      projectClaim.GetName(),
				Namespace: projectClaim.GetNamespace(),
			},
			LegalEntity: *projectClaim.Spec.LegalEntity.DeepCopy(),
		},
	}
}

func (c *CustomResourceAdapter) ProjectReferenceExists() (bool, error) {
	found := &gcpv1alpha1.ProjectReference{}
	err := c.client.Get(context.TODO(), types.NamespacedName{Name: c.projectReference.Name, Namespace: c.projectReference.Namespace}, found)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *CustomResourceAdapter) IsProjectClaimDeletion() bool {
	return c.projectClaim.DeletionTimestamp != nil
}

func (c *CustomResourceAdapter) IsProjectReferenceDeletion() bool {
	return c.projectReference.DeletionTimestamp != nil
}

func (c *CustomResourceAdapter) EnsureFinalizerDeleted() error {
	c.logger.Info("Deleting Finalizer")
	finalizers := c.projectClaim.GetFinalizers()
	if util.Contains(finalizers, ProjectClaimFinalizer) {
		c.projectClaim.SetFinalizers(util.Filter(finalizers, ProjectClaimFinalizer))
		return c.client.Update(context.TODO(), c.projectClaim)
	}
	return nil
}

func (c *CustomResourceAdapter) FinalizeProjectClaim() (ObjectState, error) {
	projectReferenceExists, err := c.ProjectReferenceExists()
	if err != nil {
		return ObjectUnchanged, err
	}

	projectReferenceDeletionRequested := c.IsProjectReferenceDeletion()
	if projectReferenceExists && !projectReferenceDeletionRequested {
		err := c.client.Delete(context.TODO(), c.projectReference)
		if err != nil {
			return ObjectUnchanged, err
		}
	}

	// Assure the finalizer is not deleted as long as ProjectReference exists
	if !projectReferenceExists {
		err := c.EnsureFinalizerDeleted()
		if err != nil {
			return ObjectUnchanged, err
		}
		return ObjectModified, nil
	}

	return ObjectUnchanged, nil
}

func (c *CustomResourceAdapter) EnsureProjectClaimInitialized() (ObjectState, error) {
	if c.projectClaim.Status.Conditions == nil {
		c.projectClaim.Status.Conditions = []gcpv1alpha1.ProjectClaimCondition{}
		err := c.client.Status().Update(context.TODO(), c.projectClaim)
		if err != nil {
			c.logger.Error(err, "Failed to initalize ProjectClaim")
			return ObjectUnchanged, err
		}
		return ObjectModified, nil
	}
	return ObjectUnchanged, nil
}

func (c *CustomResourceAdapter) EnsureProjectReferenceLink() (ObjectState, error) {
	expectedLink := gcpv1alpha1.NamespacedName{
		Name:      c.projectReference.GetName(),
		Namespace: c.projectReference.GetNamespace(),
	}
	if c.projectClaim.Spec.ProjectReferenceCRLink == expectedLink {
		return ObjectUnchanged, nil
	}
	c.projectClaim.Spec.ProjectReferenceCRLink = expectedLink
	err := c.client.Update(context.TODO(), c.projectClaim)
	if err != nil {
		return ObjectUnchanged, err
	}
	return ObjectModified, nil
}

func (c *CustomResourceAdapter) EnsureFinalizer() (ObjectState, error) {
	if !util.Contains(c.projectClaim.GetFinalizers(), ProjectClaimFinalizer) {
		c.logger.Info("Adding Finalizer to the ProjectClaim")
		c.projectClaim.SetFinalizers(append(c.projectClaim.GetFinalizers(), ProjectClaimFinalizer))

		err := c.client.Update(context.TODO(), c.projectClaim)
		if err != nil {
			c.logger.Error(err, "Failed to update ProjectClaim with finalizer")
			return ObjectUnchanged, err
		}
		return ObjectModified, nil
	}
	return ObjectUnchanged, nil
}

func (c *CustomResourceAdapter) EnsureProjectReferenceExists() error {
	projectReferenceExists, err := c.ProjectReferenceExists()
	if err != nil {
		return err
	}

	if !projectReferenceExists {
		return c.client.Create(context.TODO(), c.projectReference)
	}
	return nil
}

func (c *CustomResourceAdapter) EnsureProjectClaimState(state gcpv1alpha1.ClaimStatus) error {
	if c.projectClaim.Status.State == state {
		return nil
	}

	if state == gcpv1alpha1.ClaimStatusPending {
		if c.projectClaim.Status.State != "" {
			return nil
		}
	}

	if state == gcpv1alpha1.ClaimStatusPendingProject {
		if c.projectClaim.Status.State != gcpv1alpha1.ClaimStatusPending {
			return nil
		}
	}

	c.projectClaim.Status.State = state
	return c.StatusUpdate()
}

// StatusUpdate updates the project claim status
func (c *CustomResourceAdapter) StatusUpdate() error {
	err := c.client.Status().Update(context.TODO(), c.projectClaim)
	if err != nil {
		c.logger.Error(err, fmt.Sprintf("failed to update ProjectClaim state for %s", c.projectClaim.Name))
		return err
	}

	return nil
}

// SetProjectClaimCondition sets a condition on a ProjectClaim resource's status
func (c *CustomResourceAdapter) SetProjectClaimCondition(status corev1.ConditionStatus, reason string, message string) error {
	conditions := &c.projectClaim.Status.Conditions
	conditionType := gcpv1alpha1.ClaimConditionError
	now := metav1.Now()
	existingCondition := c.FindProjectClaimCondition()
	if existingCondition == nil {
		if status == corev1.ConditionTrue {
			*conditions = append(
				*conditions,
				gcpv1alpha1.ProjectClaimCondition{
					Type:               conditionType,
					Status:             status,
					Reason:             reason,
					Message:            message,
					LastTransitionTime: now,
					LastProbeTime:      now,
				},
			)
		}
	} else {
		// If it does not exist, assign it as now. Otherwise, do not touch
		if existingCondition.Status != status {
			existingCondition.LastTransitionTime = now
		}
		existingCondition.Status = status
		existingCondition.Reason = reason
		existingCondition.Message = message
		existingCondition.LastProbeTime = now
	}

	return c.StatusUpdate()
}

// FindProjectClaimCondition finds the suitable ProjectClaimCondition object
// by looking for adapter's condition list.
// If none exists, then returns nil.
func (c *CustomResourceAdapter) FindProjectClaimCondition() *gcpv1alpha1.ProjectClaimCondition {
	conditions := c.projectClaim.Status.Conditions
	conditionType := gcpv1alpha1.ClaimConditionError
	for i, condition := range conditions {
		if condition.Type == conditionType {
			return &conditions[i]
		}
	}

	return nil
}
