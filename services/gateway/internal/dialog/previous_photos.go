package dialog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type previousInspectionPhotoReader interface {
	PreviousInspectionPhoto(context.Context, string, string, int) (dataapi.AssetContent, error)
}

type previousPhotoRequest struct {
	recognized bool
	vehicleID  string
	version    int64
	slot       int
}

func parsePreviousPhotoTarget(event dataapi.NormalizedEvent) previousPhotoRequest {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return previousPhotoRequest{}
	}
	parts := strings.Split(*event.Payload.CallbackData, ":")
	if len(parts) == 0 || (parts[0] != "prev-photos" && parts[0] != "prev-photo") {
		return previousPhotoRequest{}
	}
	request := previousPhotoRequest{recognized: true}
	wantParts := 3
	if parts[0] == "prev-photo" {
		wantParts = 4
	}
	if len(parts) != wantParts || !vehicleIDPattern.MatchString(parts[1]) {
		return request
	}
	version, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || version < 1 {
		return request
	}
	request.vehicleID = parts[1]
	request.version = version
	if wantParts == 4 {
		request.slot, err = strconv.Atoi(parts[3])
		if err != nil || request.slot < 1 || request.slot > 8 {
			request.slot = -1
		}
	}
	return request
}

func (p Bootstrap) showPreviousInspectionPhotos(ctx context.Context, actor string, maxID int64, request previousPhotoRequest) error {
	if !request.recognized || !vehicleIDPattern.MatchString(request.vehicleID) || request.version < 1 || request.slot < 0 || request.slot > 8 {
		return p.sendView(ctx, maxID, "Кнопка фото осмотра устарела. Откройте предыдущий осмотр через /cars.", nil)
	}
	inspection, err := p.Data.PreviousInspection(ctx, actor, request.vehicleID)
	if err != nil {
		if apiErrorStatus(err, http.StatusNotFound) {
			return p.sendView(ctx, maxID, "Предыдущий осмотр изменился или больше недоступен. Откройте /cars.", nil)
		}
		return err
	}
	if !validPreviousInspectionProjection(inspection) || inspection.Version != request.version {
		return p.sendView(ctx, maxID, "Данные предыдущего осмотра изменились. Откройте его заново через /cars.", nil)
	}
	slots, ok := validPreviousPhotoSlots(inspection.OccupiedSlots)
	if !ok {
		return errors.New("data-api: invalid previous inspection photo slots")
	}
	if len(slots) == 0 {
		return p.sendView(ctx, maxID, "В предыдущем осмотре нет сохранённых фотографий.", nil)
	}
	if request.slot == 0 {
		rows := make([][]maxsdk.Button, 0, len(slots)+1)
		for _, slot := range slots {
			rows = append(rows, []maxsdk.Button{{Text: fmt.Sprintf("%d. %s", slot, photoAngles[slot-1]), Payload: fmt.Sprintf("prev-photo:%s:%d:%d", request.vehicleID, request.version, slot)}})
		}
		rows = append(rows, []maxsdk.Button{{Text: "К предыдущему осмотру", Payload: "prev:" + request.vehicleID}})
		return p.sendView(ctx, maxID, fmt.Sprintf("Выберите ракурс предыдущего осмотра (%d/8).", len(slots)), rows)
	}
	if !containsSlot(slots, request.slot) {
		return p.sendView(ctx, maxID, "Этот ракурс не сохранён в предыдущем осмотре. Откройте его заново через /cars.", nil)
	}
	reader, ok := p.Data.(previousInspectionPhotoReader)
	if !ok {
		return errors.New("dialog previous inspection photo reader is not configured")
	}
	content, err := reader.PreviousInspectionPhoto(ctx, actor, request.vehicleID, request.slot)
	if err != nil {
		if apiErrorStatus(err, http.StatusNotFound) {
			return p.sendView(ctx, maxID, "Фотография больше недоступна. Обновите предыдущий осмотр через /cars.", nil)
		}
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusServiceUnavailable && apiErr.Code == "STORAGE_UNAVAILABLE" {
			return p.sendView(ctx, maxID, "Фотографию временно не удалось прочитать. Попробуйте открыть её ещё раз через /cars.", nil)
		}
		return err
	}
	if len(content.Bytes) == 0 || len(content.Bytes) > 10<<20 || (content.ContentType != "image/jpeg" && content.ContentType != "image/png" && content.ContentType != "image/webp") {
		return errors.New("data-api: invalid previous inspection photo content")
	}
	latest, err := p.Data.PreviousInspection(ctx, actor, request.vehicleID)
	if err != nil {
		if apiErrorStatus(err, http.StatusNotFound) {
			return p.sendView(ctx, maxID, "Предыдущий осмотр изменился. Откройте его заново через /cars.", nil)
		}
		return err
	}
	if !validPreviousInspectionProjection(latest) || latest.ID != inspection.ID || latest.Version != inspection.Version || !latest.UpdatedAt.Equal(inspection.UpdatedAt) {
		return p.sendView(ctx, maxID, "Предыдущий осмотр изменился. Откройте его заново через /cars.", nil)
	}
	sender, ok := p.MAX.(maxsdk.PhotoSender)
	if !ok {
		return errors.New("MAX photo sender is not configured")
	}
	_, err = sender.SendImage(ctx, maxID, fmt.Sprintf("Предыдущий осмотр · %d/8: %s", request.slot, photoAngles[request.slot-1]), content.ContentType, content.Bytes)
	return err
}

func validPreviousInspectionProjection(inspection dataapi.Inspection) bool {
	return vehicleIDPattern.MatchString(inspection.ID) && inspection.Phase == "after" && inspection.Status == "finalized" && inspection.Version > 0
}

func validPreviousPhotoSlots(slots []int) ([]int, bool) {
	result := append([]int(nil), slots...)
	seen := make(map[int]struct{}, len(result))
	for _, slot := range result {
		if slot < 1 || slot > 8 {
			return nil, false
		}
		if _, exists := seen[slot]; exists {
			return nil, false
		}
		seen[slot] = struct{}{}
	}
	sort.Ints(result)
	return result, true
}

func apiErrorStatus(err error, status int) bool {
	var apiErr *dataapi.APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}
