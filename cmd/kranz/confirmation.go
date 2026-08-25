package main

import (
	"context"
	"errors"
	"io"

	"github.com/kranz-org/kranz/internal/app"
	kranzcli "github.com/kranz-org/kranz/internal/cli"
	kranzruntime "github.com/kranz-org/kranz/internal/runtime"
)

// executeConfirmedPlan keeps the 0.8 CLI's explicit-command semantics while
// routing it through the same resolved plan and supervisor-owned one-shot token
// machinery as MCP. MCP remains fail-closed and requires its caller to repeat
// the request; typing a concrete CLI mutation is the CLI adapter's approval.
func executeConfirmedPlan(client *kranzruntime.Client, request app.PlanRequest, options kranzcli.GlobalOptions, stdout io.Writer) (app.OperationResult, error) {
	_ = options
	_ = stdout
	result, err := client.ExecutePlan(context.Background(), request, "")
	var required *app.ConfirmationRequiredError
	if !errors.As(err, &required) {
		return result, err
	}
	return client.ExecutePlan(context.Background(), request, required.Plan.ConfirmationToken)
}
