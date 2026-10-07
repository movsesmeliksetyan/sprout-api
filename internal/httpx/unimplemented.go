package httpx

import "context"

// NotImplemented answers every API operation with 501. It is the server's
// default API, and the base that feature handlers are layered over: embed it
// one level deeper than the handlers so their methods take precedence.
//
// One method per operation in api/openapi.yaml; the assertion below fails to
// compile when the two drift apart.
type NotImplemented struct{}

var _ StrictServerInterface = NotImplemented{}

// UpdateBudgets is not implemented yet.
func (NotImplemented) UpdateBudgets(context.Context, UpdateBudgetsRequestObject) (UpdateBudgetsResponseObject, error) {
	return nil, errNotImplemented
}

// ListCategories is not implemented yet.
func (NotImplemented) ListCategories(context.Context, ListCategoriesRequestObject) (ListCategoriesResponseObject, error) {
	return nil, errNotImplemented
}

// CreateCategory is not implemented yet.
func (NotImplemented) CreateCategory(context.Context, CreateCategoryRequestObject) (CreateCategoryResponseObject, error) {
	return nil, errNotImplemented
}

// ReorderCategories is not implemented yet.
func (NotImplemented) ReorderCategories(context.Context, ReorderCategoriesRequestObject) (ReorderCategoriesResponseObject, error) {
	return nil, errNotImplemented
}

// DeleteCategory is not implemented yet.
func (NotImplemented) DeleteCategory(context.Context, DeleteCategoryRequestObject) (DeleteCategoryResponseObject, error) {
	return nil, errNotImplemented
}

// UpdateCategory is not implemented yet.
func (NotImplemented) UpdateCategory(context.Context, UpdateCategoryRequestObject) (UpdateCategoryResponseObject, error) {
	return nil, errNotImplemented
}

// SuggestCategory is not implemented yet.
func (NotImplemented) SuggestCategory(context.Context, SuggestCategoryRequestObject) (SuggestCategoryResponseObject, error) {
	return nil, errNotImplemented
}

// DeleteDevice is not implemented yet.
func (NotImplemented) DeleteDevice(context.Context, DeleteDeviceRequestObject) (DeleteDeviceResponseObject, error) {
	return nil, errNotImplemented
}

// RegisterDevice is not implemented yet.
func (NotImplemented) RegisterDevice(context.Context, RegisterDeviceRequestObject) (RegisterDeviceResponseObject, error) {
	return nil, errNotImplemented
}

// ListGoals is not implemented yet.
func (NotImplemented) ListGoals(context.Context, ListGoalsRequestObject) (ListGoalsResponseObject, error) {
	return nil, errNotImplemented
}

// CreateGoal is not implemented yet.
func (NotImplemented) CreateGoal(context.Context, CreateGoalRequestObject) (CreateGoalResponseObject, error) {
	return nil, errNotImplemented
}

// DeleteGoal is not implemented yet.
func (NotImplemented) DeleteGoal(context.Context, DeleteGoalRequestObject) (DeleteGoalResponseObject, error) {
	return nil, errNotImplemented
}

// GetGoal is not implemented yet.
func (NotImplemented) GetGoal(context.Context, GetGoalRequestObject) (GetGoalResponseObject, error) {
	return nil, errNotImplemented
}

// UpdateGoal is not implemented yet.
func (NotImplemented) UpdateGoal(context.Context, UpdateGoalRequestObject) (UpdateGoalResponseObject, error) {
	return nil, errNotImplemented
}

// ListContributions is not implemented yet.
func (NotImplemented) ListContributions(context.Context, ListContributionsRequestObject) (ListContributionsResponseObject, error) {
	return nil, errNotImplemented
}

// CreateContribution is not implemented yet.
func (NotImplemented) CreateContribution(context.Context, CreateContributionRequestObject) (CreateContributionResponseObject, error) {
	return nil, errNotImplemented
}

// DeleteContribution is not implemented yet.
func (NotImplemented) DeleteContribution(context.Context, DeleteContributionRequestObject) (DeleteContributionResponseObject, error) {
	return nil, errNotImplemented
}

// GetHome is not implemented yet.
func (NotImplemented) GetHome(context.Context, GetHomeRequestObject) (GetHomeResponseObject, error) {
	return nil, errNotImplemented
}

// CreateImport is not implemented yet.
func (NotImplemented) CreateImport(context.Context, CreateImportRequestObject) (CreateImportResponseObject, error) {
	return nil, errNotImplemented
}

// DeleteImport is not implemented yet.
func (NotImplemented) DeleteImport(context.Context, DeleteImportRequestObject) (DeleteImportResponseObject, error) {
	return nil, errNotImplemented
}

// GetImport is not implemented yet.
func (NotImplemented) GetImport(context.Context, GetImportRequestObject) (GetImportResponseObject, error) {
	return nil, errNotImplemented
}

// CommitImport is not implemented yet.
func (NotImplemented) CommitImport(context.Context, CommitImportRequestObject) (CommitImportResponseObject, error) {
	return nil, errNotImplemented
}

// SetImportMapping is not implemented yet.
func (NotImplemented) SetImportMapping(context.Context, SetImportMappingRequestObject) (SetImportMappingResponseObject, error) {
	return nil, errNotImplemented
}

// ListImportRows is not implemented yet.
func (NotImplemented) ListImportRows(context.Context, ListImportRowsRequestObject) (ListImportRowsResponseObject, error) {
	return nil, errNotImplemented
}

// UpdateImportRow is not implemented yet.
func (NotImplemented) UpdateImportRow(context.Context, UpdateImportRowRequestObject) (UpdateImportRowResponseObject, error) {
	return nil, errNotImplemented
}

// ListInsights is not implemented yet.
func (NotImplemented) ListInsights(context.Context, ListInsightsRequestObject) (ListInsightsResponseObject, error) {
	return nil, errNotImplemented
}

// DeleteMe is not implemented yet.
func (NotImplemented) DeleteMe(context.Context, DeleteMeRequestObject) (DeleteMeResponseObject, error) {
	return nil, errNotImplemented
}

// GetMe is not implemented yet.
func (NotImplemented) GetMe(context.Context, GetMeRequestObject) (GetMeResponseObject, error) {
	return nil, errNotImplemented
}

// UpdateMe is not implemented yet.
func (NotImplemented) UpdateMe(context.Context, UpdateMeRequestObject) (UpdateMeResponseObject, error) {
	return nil, errNotImplemented
}

// ListNotifications is not implemented yet.
func (NotImplemented) ListNotifications(context.Context, ListNotificationsRequestObject) (ListNotificationsResponseObject, error) {
	return nil, errNotImplemented
}

// MarkNotificationsRead is not implemented yet.
func (NotImplemented) MarkNotificationsRead(context.Context, MarkNotificationsReadRequestObject) (MarkNotificationsReadResponseObject, error) {
	return nil, errNotImplemented
}

// CreateReceipt is not implemented yet.
func (NotImplemented) CreateReceipt(context.Context, CreateReceiptRequestObject) (CreateReceiptResponseObject, error) {
	return nil, errNotImplemented
}

// DeleteReceipt is not implemented yet.
func (NotImplemented) DeleteReceipt(context.Context, DeleteReceiptRequestObject) (DeleteReceiptResponseObject, error) {
	return nil, errNotImplemented
}

// GetReceipt is not implemented yet.
func (NotImplemented) GetReceipt(context.Context, GetReceiptRequestObject) (GetReceiptResponseObject, error) {
	return nil, errNotImplemented
}

// ConfirmReceipt is not implemented yet.
func (NotImplemented) ConfirmReceipt(context.Context, ConfirmReceiptRequestObject) (ConfirmReceiptResponseObject, error) {
	return nil, errNotImplemented
}

// GetCategoriesSummary is not implemented yet.
func (NotImplemented) GetCategoriesSummary(context.Context, GetCategoriesSummaryRequestObject) (GetCategoriesSummaryResponseObject, error) {
	return nil, errNotImplemented
}

// GetCategorySummary is not implemented yet.
func (NotImplemented) GetCategorySummary(context.Context, GetCategorySummaryRequestObject) (GetCategorySummaryResponseObject, error) {
	return nil, errNotImplemented
}

// GetStatsSummary is not implemented yet.
func (NotImplemented) GetStatsSummary(context.Context, GetStatsSummaryRequestObject) (GetStatsSummaryResponseObject, error) {
	return nil, errNotImplemented
}

// ListTransactions is not implemented yet.
func (NotImplemented) ListTransactions(context.Context, ListTransactionsRequestObject) (ListTransactionsResponseObject, error) {
	return nil, errNotImplemented
}

// CreateTransaction is not implemented yet.
func (NotImplemented) CreateTransaction(context.Context, CreateTransactionRequestObject) (CreateTransactionResponseObject, error) {
	return nil, errNotImplemented
}

// DeleteTransaction is not implemented yet.
func (NotImplemented) DeleteTransaction(context.Context, DeleteTransactionRequestObject) (DeleteTransactionResponseObject, error) {
	return nil, errNotImplemented
}

// GetTransaction is not implemented yet.
func (NotImplemented) GetTransaction(context.Context, GetTransactionRequestObject) (GetTransactionResponseObject, error) {
	return nil, errNotImplemented
}

// UpdateTransaction is not implemented yet.
func (NotImplemented) UpdateTransaction(context.Context, UpdateTransactionRequestObject) (UpdateTransactionResponseObject, error) {
	return nil, errNotImplemented
}

// CreateUpload is not implemented yet.
func (NotImplemented) CreateUpload(context.Context, CreateUploadRequestObject) (CreateUploadResponseObject, error) {
	return nil, errNotImplemented
}
