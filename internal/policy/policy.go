// Package policy maps actions to permission classes.
//
// The mapping is code, not model output. A planner may describe any action it
// likes; this table decides what is allowed. Keeping it in one place makes the
// capability surface auditable in review.
package policy

import "personal-agent/internal/domain"

// Action names used across planner, gateway and executor.
const (
	ActFetchPrice  = "fetch_price"
	ActFetchOrders = "fetch_orders"
	ActFetchPage   = "fetch_page"
	ActSubmitForm  = "submit_form"
	ActPlaceOrder  = "place_order"
	ActSendMessage = "send_message"
	ActPay         = "pay"
	ActSign        = "sign_contract"
)

// RiskOf returns the permission class for an action. Unknown actions are treated
// as confirm-level rather than read: guessing low risk is the dangerous direction.
func RiskOf(action string) domain.RiskLevel {
	switch action {
	case ActFetchPrice, ActFetchOrders, ActFetchPage:
		return domain.RiskRead
	case ActSubmitForm, ActPlaceOrder:
		return domain.RiskConfirm
	case ActPay, ActSendMessage, ActSign:
		return domain.RiskForbidden
	default:
		return domain.RiskConfirm
	}
}

// IsKnown reports whether the action exists in the table at all.
func IsKnown(action string) bool {
	switch action {
	case ActFetchPrice, ActFetchOrders, ActFetchPage,
		ActSubmitForm, ActPlaceOrder, ActSendMessage, ActPay, ActSign:
		return true
	}
	return false
}

// AutoAllowedTier reports whether a confirm-level action may run without a
// human click for a given trust tier and amount. MVP keeps this closed: tier 0
// and tier 1 both require approval, and only tier 2 allows a limited amount.
func AutoAllowedTier(trust string, amountCNY float64) bool {
	if trust != "tier2" {
		return false
	}
	return amountCNY > 0 && amountCNY <= 50
}
