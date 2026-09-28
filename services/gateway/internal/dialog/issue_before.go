package dialog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

func inspectionReadyForIssueQuestion(inspection dataapi.Inspection) bool {
	return inspection.ID != "" && inspection.Phase == "before" && inspection.Status == "draft" && inspection.PhotosConfirmedAt != nil && inspection.FuelLevel != nil && inspection.OdometerKM != nil && len(inspection.OccupiedSlots) == 8 && len(inspection.MissingSlots) == 0
}

func issueAnswerTarget(event dataapi.NormalizedEvent) (string, int64, string, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "new-issues-set:") {
		return "", 0, "", false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "new-issues-set:"), ":")
	if len(parts) != 3 {
		return "", 0, "", true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 {
		return "", 0, "", true
	}
	return parts[0], version, parts[2], true
}

func (p Bootstrap) checkoutIssueAnswer(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, inspectionID string, version int64, choice string, save bool) error {
	checkout := state.Checkout
	if !vehicleIDPattern.MatchString(inspectionID) || version < 1 || checkout == nil || checkout.Status != "holding" || checkout.Step != "inspection" || checkout.Inspection.ID != inspectionID || !inspectionReadyForIssueQuestion(checkout.Inspection) {
		return p.sendView(ctx, maxID, "Осмотр изменился или hold истёк. Обновите /menu.", nil)
	}
	if !save {
		if checkout.Inspection.Version != version || checkout.NoNewIssues != nil && *checkout.NoNewIssues {
			return p.sendView(ctx, maxID, "Ответ уже изменился. Обновите /menu.", nil)
		}
		rows := [][]maxsdk.Button{
			{{Text: "Новых замечаний нет", Payload: fmt.Sprintf("new-issues-set:%s:%d:no", inspectionID, version)}},
			{{Text: "Есть замечание", Payload: fmt.Sprintf("new-issues-set:%s:%d:yes", inspectionID, version)}},
		}
		return p.sendView(ctx, maxID, "Есть новые повреждения, неисправности или загрязнение, которых нет в известных замечаниях?", rows)
	}
	if choice != "yes" && choice != "no" {
		return p.sendView(ctx, maxID, "Некорректный ответ. Откройте /menu.", nil)
	}
	if checkout.Inspection.Version != version && !(checkout.Inspection.Version == version+1 && checkout.Inspection.NewDamage != nil && *checkout.Inspection.NewDamage == (choice == "yes")) {
		return p.sendView(ctx, maxID, "Ответ уже изменился. Обновите /menu.", nil)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("issue answer requires a durable inbox lease")
	}
	lease := &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken}
	if checkout.Inspection.Version == version {
		value := choice == "yes"
		key, err := inboxworker.CommandKey(item, "inspection.update")
		if err != nil {
			return err
		}
		result, err := p.Commands.InspectionUpdate(ctx, actor, inspectionID, version, dataapi.InspectionUpdateInput{NewDamage: &value}, key, lease)
		if err != nil {
			return issueAnswerError(ctx, p, maxID, err)
		}
		updated, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
		if err != nil || updated.ID != inspectionID || updated.Version != version+1 || updated.NewDamage == nil || *updated.NewDamage != value {
			return errors.New("issue answer returned invalid inspection")
		}
	}
	if choice == "yes" {
		return p.sendView(ctx, maxID, "Новое замечание отмечено. Поездка не начнётся. Оставьте машину и сообщите ответственному за автопарк.", nil)
	}
	if checkout.NoNewIssues != nil && *checkout.NoNewIssues {
		return p.sendView(ctx, maxID, "Отсутствие новых замечаний сохранено. Продолжите через /menu.", nil)
	}
	key, err := inboxworker.CommandKey(item, "checkout.set_no_new_issues")
	if err != nil {
		return err
	}
	checkoutVersion := checkout.Version
	if checkout.Inspection.Version == version {
		checkoutVersion++
	}
	result, err := p.Commands.CheckoutSetNoNewIssues(ctx, actor, checkout.ID, checkoutVersion, key, lease)
	if err != nil {
		return issueAnswerError(ctx, p, maxID, err)
	}
	updated, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil || updated.ID != checkout.ID || updated.Version != checkoutVersion+1 || updated.NoNewIssues == nil || !*updated.NoNewIssues || updated.Inspection.NewDamage == nil || *updated.Inspection.NewDamage {
		return errors.New("no-new-issues returned invalid checkout")
	}
	return p.sendView(ctx, maxID, "Отсутствие новых замечаний сохранено. Продолжите через /menu.", nil)
}

func issueAnswerError(ctx context.Context, p Bootstrap, maxID int64, err error) error {
	var apiErr *dataapi.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
		return p.sendView(ctx, maxID, "Осмотр изменился или hold истёк. Обновите /menu.", nil)
	}
	return err
}
