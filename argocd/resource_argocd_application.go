package argocd

import (
	"context"
	"fmt"
	"strings"
	"time"

	applicationClient "github.com/argoproj/argo-cd/pkg/apiclient/application"
	application "github.com/argoproj/argo-cd/pkg/apis/application/v1alpha1"
	"github.com/argoproj/gitops-engine/pkg/health"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceArgoCDApplication() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceArgoCDApplicationCreate,
		ReadContext:   resourceArgoCDApplicationRead,
		UpdateContext: resourceArgoCDApplicationUpdate,
		DeleteContext: resourceArgoCDApplicationDelete,
		// TODO: add importer acceptance tests
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: map[string]*schema.Schema{
			"metadata": metadataSchema("applications.argoproj.io"),
			"spec":     applicationSpecSchema(),
			"wait": {
				Type:        schema.TypeBool,
				Description: "Upon application creation or update, wait for application health/sync status to be healthy/Synced, upon application deletion, wait for application to be removed, when set to true.",
				Optional:    true,
				Default:     false,
			},
		},
		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(5 * time.Minute),
			Update: schema.DefaultTimeout(5 * time.Minute),
			Delete: schema.DefaultTimeout(5 * time.Minute),
		},
	}
}

func resourceArgoCDApplicationCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	objectMeta, spec, diags := expandApplication(d)
	if diags != nil {
		return diags
	}
	server := meta.(*ServerInterface)
	if err := server.initClients(); err != nil {
		return []diag.Diagnostic{
			diag.Diagnostic{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("Failed to init clients"),
				Detail:   err.Error(),
			},
		}
	}
	c := *server.ApplicationClient
	app, err := c.Get(ctx, &applicationClient.ApplicationQuery{
		Name: &objectMeta.Name,
	})
	if err != nil && !strings.Contains(err.Error(), "NotFound") {
		return []diag.Diagnostic{
			{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("application %s could not be created", objectMeta.Name),
				Detail:   err.Error(),
			},
		}
	}
	if app != nil {
		switch app.DeletionTimestamp {
		case nil:
		default:
			// Pre-existing app is still in Kubernetes soft deletion queue
			time.Sleep(time.Duration(*app.DeletionGracePeriodSeconds))
		}
	}

	featureApplicationLevelSyncOptionsSupported, err := server.isFeatureSupported(featureApplicationLevelSyncOptions)
	if err != nil {
		return []diag.Diagnostic{
			{
				Severity: diag.Error,
				Summary:  "feature not supported",
				Detail:   err.Error(),
			},
		}
	}
	if !featureApplicationLevelSyncOptionsSupported &&
		spec.SyncPolicy != nil &&
		spec.SyncPolicy.SyncOptions != nil {
		return []diag.Diagnostic{
			{
				Severity: diag.Error,
				Summary: fmt.Sprintf(
					"application-level sync_options is only supported from ArgoCD %s onwards",
					featureVersionConstraintsMap[featureApplicationLevelSyncOptions].String()),
				Detail: err.Error(),
			},
		}
	}

	app, err = c.Create(ctx, &applicationClient.ApplicationCreateRequest{
		Application: application.Application{
			ObjectMeta: objectMeta,
			Spec:       spec,
		},
	})
	if err != nil {
		return []diag.Diagnostic{
			{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("application %s could not be created", objectMeta.Name),
				Detail:   err.Error(),
			},
		}
	}
	if app == nil {
		return []diag.Diagnostic{
			{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("application %s could not be created: unknown reason", objectMeta.Name),
			},
		}
	}
	d.SetId(app.Name)
	if wait, ok := d.GetOk("wait"); ok && wait.(bool) {
		err = resource.RetryContext(ctx, d.Timeout(schema.TimeoutCreate), func() *resource.RetryError {
			a, err := c.Get(ctx, &applicationClient.ApplicationQuery{
				Name: &app.Name,
			})
			if err != nil {
				return resource.NonRetryableError(fmt.Errorf("error while waiting for application %s to be synced and healthy: %s", app.Name, err))
			}
			if a.Status.Health.Status != health.HealthStatusHealthy {
				return resource.RetryableError(fmt.Errorf("expected application health status to be healthy but was %s", a.Status.Health.Status))
			}
			if a.Status.Sync.Status != application.SyncStatusCodeSynced {
				return resource.RetryableError(fmt.Errorf("expected application sync status to be synced but was %s", a.Status.Sync.Status))
			}
			return nil
		})
		if err != nil {
			return []diag.Diagnostic{
				{
					Severity: diag.Error,
					Summary:  fmt.Sprintf("Error while waiting for application %s to be created", objectMeta.Name),
					Detail:   err.Error(),
				},
			}
		}
	}
	return resourceArgoCDApplicationRead(ctx, d, meta)
}

func resourceArgoCDApplicationRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	server := meta.(*ServerInterface)
	if err := server.initClients(); err != nil {
		return []diag.Diagnostic{
			diag.Diagnostic{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("Failed to init clients"),
				Detail:   err.Error(),
			},
		}
	}
	c := *server.ApplicationClient
	appName := d.Id()
	app, err := c.Get(ctx, &applicationClient.ApplicationQuery{
		Name: &appName,
	})
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			d.SetId("")
			return diag.Diagnostics{}
		}
		return []diag.Diagnostic{
			{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("application %s not found", appName),
				Detail:   err.Error(),
			},
		}
	}
	err = flattenApplication(app, d)
	if err != nil {
		return []diag.Diagnostic{
			{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("application %s could not be flattened", appName),
				Detail:   err.Error(),
			},
		}
	}
	return nil
}

func resourceArgoCDApplicationUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	appName := d.Id()
	if ok := d.HasChanges("metadata", "spec"); ok {
		_, spec, diags := expandApplication(d)
		if diags != nil {
			return diags
		}
		server := meta.(*ServerInterface)
		if err := server.initClients(); err != nil {
			return []diag.Diagnostic{
				diag.Diagnostic{
					Severity: diag.Error,
					Summary:  fmt.Sprintf("Failed to init clients"),
					Detail:   err.Error(),
				},
			}
		}
		c := *server.ApplicationClient
		appRequest, err := c.Get(ctx, &applicationClient.ApplicationQuery{
			Name:     &appName,
			Projects: []string{spec.Project},
		})

		featureApplicationLevelSyncOptionsSupported, err := server.isFeatureSupported(featureApplicationLevelSyncOptions)
		if err != nil {
			return []diag.Diagnostic{
				{
					Severity: diag.Error,
					Summary:  "Feature not supported",
					Detail:   err.Error(),
				},
			}
		}
		if !featureApplicationLevelSyncOptionsSupported &&
			spec.SyncPolicy != nil &&
			spec.SyncPolicy.SyncOptions != nil {
			return []diag.Diagnostic{
				{
					Severity: diag.Error,
					Summary: fmt.Sprintf(
						"application-level sync_options is only supported from ArgoCD %s onwards",
						featureVersionConstraintsMap[featureApplicationLevelSyncOptions].String()),
					Detail: err.Error(),
				},
			}
		}

		app, err := c.Get(ctx, &applicationClient.ApplicationQuery{
			Name: &appName,
		})
		if app != nil {
			// Kubernetes API requires providing the up-to-date correct ResourceVersion for updates
			appRequest.ResourceVersion = app.ResourceVersion
		}
		_, err = c.Update(ctx, &applicationClient.ApplicationUpdateRequest{
			Application: appRequest,
		})
		if err != nil {
			return []diag.Diagnostic{
				{
					Severity: diag.Error,
					Summary:  fmt.Sprintf("application %s could not be updated", appName),
					Detail:   err.Error(),
				},
			}
		}
		if wait, _ok := d.GetOk("wait"); _ok && wait.(bool) {
			err = resource.RetryContext(ctx, d.Timeout(schema.TimeoutUpdate), func() *resource.RetryError {
				a, err := c.Get(ctx, &applicationClient.ApplicationQuery{
					Name: &app.Name,
				})
				if err != nil {
					return resource.NonRetryableError(fmt.Errorf("error while waiting for application %s to be synced and healthy: %s", app.Name, err))
				}
				if a.Status.Health.Status != health.HealthStatusHealthy {
					return resource.RetryableError(fmt.Errorf("expected application health status to be healthy but was %s", a.Status.Health.Status))
				}
				if a.Status.Sync.Status != application.SyncStatusCodeSynced {
					return resource.RetryableError(fmt.Errorf("expected application sync status to be synced but was %s", a.Status.Sync.Status))
				}
				return nil
			})
			if err != nil {
				return []diag.Diagnostic{
					{
						Severity: diag.Error,
						Summary:  fmt.Sprintf("something went wrong upon waiting for the application to be updated: %s", err),
						Detail:   err.Error(),
					},
				}
			}
		}
	}
	return resourceArgoCDApplicationRead(ctx, d, meta)
}

func resourceArgoCDApplicationDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	server := meta.(*ServerInterface)
	if err := server.initClients(); err != nil {
		return []diag.Diagnostic{
			diag.Diagnostic{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("Failed to init clients"),
				Detail:   err.Error(),
			},
		}
	}
	c := *server.ApplicationClient
	appName := d.Id()
	_, err := c.Delete(ctx, &applicationClient.ApplicationDeleteRequest{
		Name: &appName,
	})
	if err != nil && !strings.Contains(err.Error(), "NotFound") {
		return []diag.Diagnostic{
			{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("application %s could not be deleted", appName),
				Detail:   err.Error(),
			},
		}
	}
	if wait, ok := d.GetOk("wait"); ok && wait.(bool) {
		err = resource.RetryContext(ctx, d.Timeout(schema.TimeoutDelete), func() *resource.RetryError {
			_, err := c.Get(ctx, &applicationClient.ApplicationQuery{
				Name: &appName,
			})
			if err == nil {
				return resource.RetryableError(fmt.Errorf("application %s is still present", appName))
			}
			if !strings.Contains(err.Error(), "NotFound") {
				return resource.NonRetryableError(err)
			}
			d.SetId("")
			return nil
		})
		if err != nil {
			return []diag.Diagnostic{
				{
					Severity: diag.Error,
					Summary:  fmt.Sprintf("application %s not be deleted", appName),
					Detail:   err.Error(),
				},
			}
		}
	}
	d.SetId("")
	return nil
}
