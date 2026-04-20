package gateway

import (
	"context"
	"fmt"
	"strings"

	"gomodel/internal/core"
)

// BatchExecutionSelection captures the provider and workflow selector for a native batch.
type BatchExecutionSelection struct {
	ProviderType string
	Selector     core.WorkflowSelector
}

// DetermineBatchExecutionSelection resolves a native batch to one provider.
func DetermineBatchExecutionSelection(
	provider core.RoutableProvider,
	resolver ModelResolver,
	req *core.BatchRequest,
) (BatchExecutionSelection, error) {
	return DetermineBatchExecutionSelectionWithAuthorizer(context.Background(), provider, resolver, nil, req)
}

// DetermineBatchExecutionSelectionWithAuthorizer resolves and authorizes native batch items.
func DetermineBatchExecutionSelectionWithAuthorizer(
	ctx context.Context,
	provider core.RoutableProvider,
	resolver ModelResolver,
	authorizer ModelAuthorizer,
	req *core.BatchRequest,
) (BatchExecutionSelection, error) {
	if req == nil {
		return BatchExecutionSelection{}, core.NewInvalidRequestError("batch request is required", nil)
	}
	if provider == nil {
		return BatchExecutionSelection{}, core.NewInvalidRequestError("provider is not configured", nil)
	}

	if strings.TrimSpace(req.InputFileID) != "" {
		if req.Metadata == nil {
			return BatchExecutionSelection{}, core.NewInvalidRequestError("metadata.provider is required for input_file_id batches", nil)
		}
		providerType := strings.TrimSpace(req.Metadata["provider"])
		if providerType == "" {
			return BatchExecutionSelection{}, core.NewInvalidRequestError("metadata.provider is required for input_file_id batches", nil)
		}
		return BatchExecutionSelection{
			ProviderType: providerType,
			Selector:     core.NewWorkflowSelector(WorkflowProviderNameForType(provider, providerType), ""),
		}, nil
	}

	if len(req.Requests) == 0 {
		return BatchExecutionSelection{}, core.NewInvalidRequestError("requests is required and must not be empty", nil)
	}

	var (
		providerType   string
		providerName   string
		commonModel    string
		hasCommonModel = true
	)
	for i, item := range req.Requests {
		requested, err := core.BatchItemRequestedModelSelector(req.Endpoint, item)
		if err != nil {
			return BatchExecutionSelection{}, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: %s", i, err.Error()), err)
		}
		resolvedSelector, _, err := ResolveExecutionSelector(provider, resolver, requested)
		if err != nil {
			return BatchExecutionSelection{}, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: %s", i, err.Error()), err)
		}
		model := resolvedSelector.QualifiedModel()
		if model == "" {
			return BatchExecutionSelection{}, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: model is required", i), nil)
		}
		if !provider.Supports(model) {
			return BatchExecutionSelection{}, core.NewInvalidRequestError("unsupported model: "+model, nil)
		}
		if authorizer != nil {
			if err := authorizer.ValidateModelAccess(ctx, resolvedSelector); err != nil {
				return BatchExecutionSelection{}, err
			}
		}
		itemProvider := provider.GetProviderType(model)
		if providerType == "" {
			providerType = itemProvider
		} else if providerType != itemProvider {
			return BatchExecutionSelection{}, core.NewInvalidRequestError("native batch supports a single provider per batch; split mixed-provider requests", nil)
		}
		itemProviderName := ResolvedProviderName(provider, resolvedSelector, resolvedSelector.Provider)
		if providerName == "" {
			providerName = itemProviderName
		} else if itemProviderName != "" && providerName != itemProviderName {
			return BatchExecutionSelection{}, core.NewInvalidRequestError("native batch supports a single configured provider per batch; split mixed-provider requests", nil)
		}

		if !hasCommonModel {
			continue
		}
		resolvedModel := strings.TrimSpace(resolvedSelector.Model)
		if resolvedModel == "" {
			hasCommonModel = false
			continue
		}
		if commonModel == "" {
			commonModel = resolvedModel
			continue
		}
		if commonModel != resolvedModel {
			hasCommonModel = false
		}
	}

	if providerType == "" {
		return BatchExecutionSelection{}, core.NewInvalidRequestError("unable to resolve provider for batch", nil)
	}
	if providerName == "" {
		providerName = WorkflowProviderNameForType(provider, providerType)
	}
	if !hasCommonModel {
		commonModel = ""
	}
	return BatchExecutionSelection{
		ProviderType: providerType,
		Selector:     core.NewWorkflowSelector(providerName, commonModel),
	}, nil
}
