package dialog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type Reader interface {
	Me(context.Context, string) (dataapi.Me, error)
	State(context.Context, string) (dataapi.CurrentState, error)
	CurrentRules(context.Context, string) (dataapi.Rules, error)
	Vehicles(context.Context, string, dataapi.VehicleFilter) (dataapi.Page[dataapi.Vehicle], error)
	Vehicle(context.Context, string, string) (dataapi.Vehicle, error)
	PreviousInspection(context.Context, string, string) (dataapi.Inspection, error)
	Issue(context.Context, string, string) (dataapi.Issue, error)
}

type CheckoutCommander interface {
	CheckoutCreate(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	CheckoutCancel(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	ChallengeCreateTake(context.Context, string, string, int64, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	ChallengeAnswer(context.Context, string, string, int64, int, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	CheckoutAcceptRules(context.Context, string, string, int64, string, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	InspectionConfirmPhotos(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	InspectionUpdate(context.Context, string, string, int64, dataapi.InspectionUpdateInput, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	CheckoutSetNoNewIssues(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	CheckoutStart(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	TripBeginReturn(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	ChallengeCreateReturn(context.Context, string, string, int64, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	ReturnCancel(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	ReturnSetLocation(context.Context, string, string, int64, string, *dataapi.InboxLease, dataapi.LocationInput) (dataapi.CommandResult, error)
	ReturnComplete(context.Context, string, string, int64, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	ConversationSave(context.Context, string, string, int64, dataapi.ConversationSaveInput, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
	IssueCreate(context.Context, string, string, int64, dataapi.IssueCreateInput, string, *dataapi.InboxLease) (dataapi.CommandResult, error)
}

type PhotoFetcher interface {
	Download(context.Context, string) (maxsdk.DownloadedPhoto, error)
}

type PhotoStore interface {
	UploadInspectionPhoto(context.Context, string, dataapi.InspectionPhotoInput) (dataapi.PhotoUploadResult, error)
	StageIssueAsset(context.Context, string, dataapi.IssueStageInput) (dataapi.StagedAsset, error)
}

// Bootstrap handles implemented menu, catalog and checkout entry events. Other
// accepted events remain durable and unacknowledged until their flow exists.
type Bootstrap struct {
	Data       Reader
	Commands   CheckoutCommander
	MAX        maxsdk.Transport
	Photos     PhotoFetcher
	PhotoStore PhotoStore
	Location   *time.Location
	MapBotName string
}

var vehicleIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var mapBotNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,80}$`)

func ValidMapBotName(name string) bool { return mapBotNamePattern.MatchString(name) }

func mapLaunchURL(botName, returnID string) string {
	if !ValidMapBotName(botName) || !vehicleIDPattern.MatchString(returnID) {
		return ""
	}
	return "https://max.ru/" + botName + "?startapp=" + returnID
}

func (p Bootstrap) Handle(ctx context.Context, item dataapi.InboxClaimItem) error {
	pageNumber, catalog := catalogPage(item.Event)
	vehicleID, expectedVersion, card := cardTarget(item.Event)
	previousVehicleID, previous := previousTarget(item.Event)
	actionVehicleID, actionVersion, intent := vehicleActionTarget(item.Event, "intent:")
	confirmVehicleID, confirmVersion, confirm := vehicleActionTarget(item.Event, "take:")
	cancelID, cancelVersion, cancelIntent := vehicleActionTarget(item.Event, "cancel-intent:")
	confirmedCancelID, confirmedCancelVersion, confirmedCancel := vehicleActionTarget(item.Event, "cancel:")
	mathCheckoutID, mathVersion, math := vehicleActionTarget(item.Event, "math:")
	challengeID, challengeVersion, selectedOption, answer := answerTarget(item.Event)
	rulesID, rulesVersion, rules := vehicleActionTarget(item.Event, "rules:")
	acceptCheckoutID, acceptVersion, acceptedRulesID, acceptRules := acceptRulesTarget(item.Event)
	photoCheckoutID, photoVersion, photos := vehicleActionTarget(item.Event, "photos:")
	fuelInspectionID, fuelVersion, fuel := vehicleActionTarget(item.Event, "fuel:")
	setFuelID, setFuelVersion, fuelLevel, setFuel := fuelChoiceTarget(item.Event)
	odometerInspectionID, odometerVersion, odometerPrompt := vehicleActionTarget(item.Event, "odometer:")
	odometerText, odometerCommand := odometerInput(item.Event)
	issueQuestionID, issueQuestionVersion, issueQuestion := vehicleActionTarget(item.Event, "new-issues:")
	issueChoiceID, issueChoiceVersion, issueChoice, issueAnswer := issueAnswerTarget(item.Event)
	issueDraftID, issueDraftVersion, issueDraftPrompt := vehicleActionTarget(item.Event, "issue-draft:")
	issueKindID, issueKindVersion, issueKind, issueKindPrompt := issueCategoryTarget(item.Event)
	issueDraftText, issueDraftCommand := issueDraftInput(item.Event)
	tripIssueID, tripIssueVersion, tripIssueCategory, tripIssuePrompt := tripIssueTarget(item.Event)
	postReturnIssueID, postReturnIssueVersion, postReturnIssueOpen := vehicleActionTarget(item.Event, "trip-post-issue:")
	postReturnCategoryID, postReturnCategoryVersion, postReturnCategory, postReturnCategoryPrompt := postReturnIssueCategoryTarget(item.Event)
	postReturnPhotoID, postReturnPhotoVersion, postReturnPhotoHelp := vehicleActionTarget(item.Event, "trip-post-issue-photos:")
	postReturnReviewID, postReturnReviewVersion, postReturnReview := vehicleActionTarget(item.Event, "trip-post-issue-review:")
	postReturnSubmitID, postReturnSubmitVersion, postReturnSend := vehicleActionTarget(item.Event, "trip-post-issue-submit:")
	returnIssueID, returnIssueVersion, returnIssueCategory, returnIssuePrompt := returnIssueTarget(item.Event)
	returnIssuePhotoID, returnIssuePhotoVersion, returnIssuePhotoHelp := vehicleActionTarget(item.Event, "return-issue-photos:")
	returnIssueReviewID, returnIssueReviewVersion, returnIssueReview := vehicleActionTarget(item.Event, "return-issue-review:")
	returnIssueSubmitID, returnIssueSubmitVersion, returnIssueSend := vehicleActionTarget(item.Event, "return-issue-submit:")
	returnGeoID, returnGeoVersion, returnGeoConfirm := vehicleActionTarget(item.Event, "return-geo-confirm:")
	returnSummaryID, returnSummaryVersion, returnSummary := vehicleActionTarget(item.Event, "return-summary:")
	returnCompleteID, returnCompleteVersion, returnComplete := vehicleActionTarget(item.Event, "return-complete:")
	tripIssuePhotoID, tripIssuePhotoVersion, tripIssuePhotoHelp := vehicleActionTarget(item.Event, "trip-issue-photos:")
	tripIssueReviewID, tripIssueReviewVersion, tripIssueReview := vehicleActionTarget(item.Event, "trip-issue-review:")
	tripIssueSubmitID, tripIssueSubmitVersion, tripIssueSend := vehicleActionTarget(item.Event, "trip-issue-submit:")
	issuePhotoID, issuePhotoVersion, issuePhotoHelp := vehicleActionTarget(item.Event, "issue-photos:")
	issueReviewID, issueReviewVersion, issueReview := vehicleActionTarget(item.Event, "issue-review:")
	issueSubmitID, issueSubmitVersion, issueSubmit := vehicleActionTarget(item.Event, "issue-submit:")
	summaryID, summaryVersion, summary := vehicleActionTarget(item.Event, "checkout-summary:")
	startID, startVersion, start := vehicleActionTarget(item.Event, "checkout-start:")
	returnIntentID, returnIntentVersion, returnIntent := vehicleActionTarget(item.Event, "return-intent:")
	returnConfirmID, returnConfirmVersion, returnConfirm := vehicleActionTarget(item.Event, "return-confirm:")
	returnMathID, returnMathVersion, returnMath := vehicleActionTarget(item.Event, "return-math:")
	returnCancelIntentID, returnCancelIntentVersion, returnCancelIntent := vehicleActionTarget(item.Event, "return-cancel-intent:")
	returnCancelID, returnCancelVersion, returnCancel := vehicleActionTarget(item.Event, "return-cancel:")
	returnCheckID, returnCheckVersion, returnCheck := vehicleActionTarget(item.Event, "return-check:")
	returnSetID, returnSetVersion, returnField, returnValue, _, returnSet := returnCheckAnswerTarget(item.Event)
	returnPhotosID, returnPhotosVersion, returnPhotos := vehicleActionTarget(item.Event, "return-photos:")
	returnConfirmPhotosID, returnConfirmPhotosVersion, returnConfirmPhotos := vehicleActionTarget(item.Event, "return-confirm-photos:")
	returnReplaceID, returnReplaceVersion, returnReplace := vehicleActionTarget(item.Event, "return-replace:")
	returnReplaceSlotID, returnReplaceSlotVersion, returnReplaceSlotNumber, returnReplaceSlot := returnReplacementSlotTarget(item.Event)
	returnFuelID, returnFuelVersion, returnFuel := vehicleActionTarget(item.Event, "return-fuel:")
	returnFuelSetID, returnFuelSetVersion, returnFuelLevel, returnFuelSet := returnFuelChoiceTarget(item.Event)
	returnOdometerID, returnOdometerVersion, returnOdometerPrompt := vehicleActionTarget(item.Event, "return-odometer:")
	confirmInspectionID, confirmInspectionVersion, confirmPhotos := vehicleActionTarget(item.Event, "confirm-photos:")
	replaceCheckoutID, replaceVersion, replacePhotos := vehicleActionTarget(item.Event, "replace-photos:")
	replaceSlotCheckoutID, replaceSlotVersion, selectedSlot, replaceSlot := replacePhotoSlotTarget(item.Event)
	photoMessage := item.Event.EventType == "message_created" && item.Event.Payload.Kind == "photo" && item.Event.Payload.AttachmentCount == 1 && item.Event.Payload.PhotoSourceKey != nil
	geoMessage := returnGeoEvent(item.Event)
	tripView := parseTripView(item.Event)
	if !catalog && !card && !previous && !intent && !confirm && !cancelIntent && !confirmedCancel && !math && !answer && !rules && !acceptRules && !photos && !fuel && !setFuel && !odometerPrompt && !odometerCommand && !returnOdometerPrompt && !issueQuestion && !issueAnswer && !issueDraftPrompt && !issueKindPrompt && !issueDraftCommand && !tripIssuePrompt && !postReturnIssueOpen && !postReturnCategoryPrompt && !postReturnPhotoHelp && !postReturnReview && !postReturnSend && !returnIssuePrompt && !tripIssuePhotoHelp && !returnIssuePhotoHelp && !tripIssueReview && !tripIssueSend && !returnIssueReview && !returnIssueSend && !returnGeoConfirm && !returnSummary && !returnComplete && !issuePhotoHelp && !issueReview && !issueSubmit && !summary && !start && !returnIntent && !returnConfirm && !returnMath && !returnCancelIntent && !returnCancel && !returnCheck && !returnSet && !returnPhotos && !returnConfirmPhotos && !returnReplace && !returnReplaceSlot && !returnFuel && !returnFuelSet && !confirmPhotos && !replacePhotos && !replaceSlot && !photoMessage && !geoMessage && !tripView.recognized && !isMenuEvent(item.Event) {
		return inboxworker.ErrDeferred
	}
	if p.Data == nil || p.MAX == nil {
		return errors.New("dialog bootstrap is not configured")
	}
	actor := item.Event.ActorMaxUserID
	maxID, err := strconv.ParseInt(actor, 10, 64)
	if err != nil || maxID <= 0 {
		return errors.New("invalid dialog actor")
	}
	if item.Event.EventType == "message_callback" && item.Event.CallbackID != nil {
		// Callback acknowledgement is best effort: retrying this event after a
		// successful message send would duplicate the dialog response.
		_ = p.MAX.AnswerCallback(ctx, *item.Event.CallbackID)
	}
	me, err := p.Data.Me(ctx, actor)
	if err != nil {
		return err
	}
	if !me.Allowed || me.Employee == nil {
		_, err = p.MAX.SendText(ctx, maxID, "Доступ ещё не выдан. Передайте ответственному за автопарк ваш ID: "+actor)
		return err
	}
	state, err := p.Data.State(ctx, actor)
	if err != nil {
		return err
	}
	if postReturnIssueOpen {
		return p.startPostReturnIssue(ctx, actor, maxID, *me.Employee, postReturnIssueID, postReturnIssueVersion)
	}
	if postReturnCategoryPrompt {
		return p.selectPostReturnIssueCategory(ctx, item, actor, maxID, *me.Employee, state, postReturnCategoryID, postReturnCategoryVersion, postReturnCategory)
	}
	if issueDraftCommand && state.Conversation != nil && state.Conversation.Flow == postReturnIssueFlow {
		return p.postReturnIssueDraft(ctx, item, actor, maxID, *me.Employee, state, issueDraftText)
	}
	if returnIssuePrompt || issueDraftCommand && state.Return != nil && state.Trip != nil && state.Trip.Status == "returning" {
		return p.returnIssueDraft(ctx, item, actor, maxID, *me.Employee, state, returnIssueID, returnIssueVersion, returnIssueCategory, issueDraftText, issueDraftCommand)
	}
	if tripIssuePrompt || issueDraftCommand && state.Trip != nil && state.Trip.Status == "active" {
		return p.tripIssueDraft(ctx, item, actor, maxID, *me.Employee, state, tripIssueID, tripIssueVersion, tripIssueCategory, issueDraftText, issueDraftCommand)
	}
	if tripIssueReview || tripIssueSend {
		tripID, version := tripIssueReviewID, tripIssueReviewVersion
		if tripIssueSend {
			tripID, version = tripIssueSubmitID, tripIssueSubmitVersion
		}
		return p.tripIssueSubmit(ctx, item, actor, maxID, *me.Employee, state, tripID, version, tripIssueSend)
	}
	if returnIssueReview || returnIssueSend {
		inspectionID, version := returnIssueReviewID, returnIssueReviewVersion
		if returnIssueSend {
			inspectionID, version = returnIssueSubmitID, returnIssueSubmitVersion
		}
		return p.returnIssueSubmit(ctx, item, actor, maxID, *me.Employee, state, inspectionID, version, returnIssueSend)
	}
	if postReturnReview || postReturnSend {
		tripID, version := postReturnReviewID, postReturnReviewVersion
		if postReturnSend {
			tripID, version = postReturnSubmitID, postReturnSubmitVersion
		}
		return p.postReturnIssueSubmit(ctx, item, actor, maxID, *me.Employee, state, tripID, version, postReturnSend)
	}
	if postReturnPhotoHelp {
		return p.postReturnIssuePhotoHelp(ctx, actor, maxID, *me.Employee, state, postReturnPhotoID, postReturnPhotoVersion)
	}
	if geoMessage {
		return p.returnGeoDraft(ctx, item, actor, maxID, *me.Employee, state)
	}
	if returnGeoConfirm {
		return p.returnGeoConfirm(ctx, item, actor, maxID, *me.Employee, state, returnGeoID, returnGeoVersion)
	}
	if returnSummary || returnComplete {
		returnID, version := returnSummaryID, returnSummaryVersion
		if returnComplete {
			returnID, version = returnCompleteID, returnCompleteVersion
		}
		return p.returnSummaryComplete(ctx, item, actor, maxID, *me.Employee, state, returnID, version, returnComplete)
	}
	if tripView.recognized {
		return p.showTripView(ctx, actor, maxID, *me.Employee, tripView)
	}
	if returnIntent || returnConfirm {
		tripID, version := returnIntentID, returnIntentVersion
		if returnConfirm {
			tripID, version = returnConfirmID, returnConfirmVersion
		}
		return p.beginReturn(ctx, item, actor, maxID, *me.Employee, state, tripID, version, returnConfirm)
	}
	if returnCancelIntent || returnCancel {
		returnID, version := returnCancelIntentID, returnCancelIntentVersion
		if returnCancel {
			returnID, version = returnCancelID, returnCancelVersion
		}
		return p.cancelReturn(ctx, item, actor, maxID, *me.Employee, state, returnID, version, returnCancel)
	}
	if returnCheck || returnSet {
		inspectionID, version := returnCheckID, returnCheckVersion
		if returnSet {
			inspectionID, version = returnSetID, returnSetVersion
		}
		return p.returnChecklist(ctx, item, actor, maxID, *me.Employee, state, inspectionID, version, returnField, returnValue, returnSet)
	}
	if returnPhotos {
		return p.returnPhotos(ctx, maxID, *me.Employee, state, returnPhotosID, returnPhotosVersion)
	}
	if returnConfirmPhotos {
		return p.confirmReturnPhotos(ctx, item, actor, maxID, *me.Employee, state, returnConfirmPhotosID, returnConfirmPhotosVersion)
	}
	if returnReplace {
		return p.chooseReturnReplacement(ctx, maxID, *me.Employee, state, returnReplaceID, returnReplaceVersion)
	}
	if returnReplaceSlot {
		return p.requestReturnReplacement(ctx, maxID, *me.Employee, state, returnReplaceSlotID, returnReplaceSlotVersion, returnReplaceSlotNumber)
	}
	if returnFuel || returnFuelSet {
		inspectionID, version := returnFuelID, returnFuelVersion
		if returnFuelSet {
			inspectionID, version = returnFuelSetID, returnFuelSetVersion
		}
		return p.returnFuel(ctx, item, actor, maxID, *me.Employee, state, inspectionID, version, returnFuelLevel, returnFuelSet)
	}
	if returnOdometerPrompt || odometerCommand && state.Return != nil {
		return p.returnOdometer(ctx, item, actor, maxID, *me.Employee, state, returnOdometerID, returnOdometerVersion, odometerText, odometerCommand)
	}
	if intent || confirm {
		vehicleID, version := actionVehicleID, actionVersion
		if confirm {
			vehicleID, version = confirmVehicleID, confirmVersion
		}
		return p.checkoutIntent(ctx, item, actor, maxID, *me.Employee, state, vehicleID, version, confirm)
	}
	if cancelIntent || confirmedCancel {
		checkoutID, version := cancelID, cancelVersion
		if confirmedCancel {
			checkoutID, version = confirmedCancelID, confirmedCancelVersion
		}
		return p.cancelCheckout(ctx, item, actor, maxID, state, checkoutID, version, confirmedCancel)
	}
	if math {
		return p.createMath(ctx, item, actor, maxID, state, mathCheckoutID, mathVersion)
	}
	if returnMath {
		return p.createReturnMath(ctx, item, actor, maxID, *me.Employee, state, returnMathID, returnMathVersion)
	}
	if answer {
		if state.Return != nil && state.Trip != nil {
			return p.answerReturnMath(ctx, item, actor, maxID, *me.Employee, state, challengeID, challengeVersion, selectedOption)
		}
		return p.answerMath(ctx, item, actor, maxID, state, challengeID, challengeVersion, selectedOption)
	}
	if rules || acceptRules {
		checkoutID, version := rulesID, rulesVersion
		if acceptRules {
			checkoutID, version = acceptCheckoutID, acceptVersion
		}
		return p.checkoutRules(ctx, item, actor, maxID, state, checkoutID, version, acceptedRulesID, acceptRules)
	}
	if photos {
		return p.checkoutPhotos(ctx, maxID, state, photoCheckoutID, photoVersion)
	}
	if fuel || setFuel {
		inspectionID, version := fuelInspectionID, fuelVersion
		if setFuel {
			inspectionID, version = setFuelID, setFuelVersion
		}
		return p.checkoutFuel(ctx, item, actor, maxID, state, inspectionID, version, fuelLevel, setFuel)
	}
	if odometerPrompt || odometerCommand {
		return p.checkoutOdometer(ctx, item, actor, maxID, state, odometerInspectionID, odometerVersion, odometerText, odometerCommand)
	}
	if issueQuestion || issueAnswer {
		inspectionID, version := issueQuestionID, issueQuestionVersion
		if issueAnswer {
			inspectionID, version = issueChoiceID, issueChoiceVersion
		}
		return p.checkoutIssueAnswer(ctx, item, actor, maxID, state, inspectionID, version, issueChoice, issueAnswer)
	}
	if issueDraftPrompt || issueKindPrompt || issueDraftCommand {
		inspectionID, version := issueDraftID, issueDraftVersion
		if issueKindPrompt {
			inspectionID, version = issueKindID, issueKindVersion
		}
		return p.checkoutIssueDraft(ctx, item, actor, maxID, *me.Employee, state, inspectionID, version, issueKind, issueDraftText, issueDraftCommand)
	}
	if issuePhotoHelp {
		return p.issuePhotoHelp(ctx, maxID, state, issuePhotoID, issuePhotoVersion)
	}
	if tripIssuePhotoHelp {
		return p.issuePhotoHelp(ctx, maxID, state, tripIssuePhotoID, tripIssuePhotoVersion)
	}
	if returnIssuePhotoHelp {
		return p.issuePhotoHelp(ctx, maxID, state, returnIssuePhotoID, returnIssuePhotoVersion)
	}
	if issueReview || issueSubmit {
		inspectionID, version := issueReviewID, issueReviewVersion
		if issueSubmit {
			inspectionID, version = issueSubmitID, issueSubmitVersion
		}
		return p.checkoutIssueSubmit(ctx, item, actor, maxID, *me.Employee, state, inspectionID, version, issueSubmit)
	}
	if summary || start {
		checkoutID, version := summaryID, summaryVersion
		if start {
			checkoutID, version = startID, startVersion
		}
		return p.checkoutSummaryStart(ctx, item, actor, maxID, *me.Employee, state, checkoutID, version, start)
	}
	if confirmPhotos {
		return p.confirmCheckoutPhotos(ctx, item, actor, maxID, state, confirmInspectionID, confirmInspectionVersion)
	}
	if replacePhotos {
		return p.chooseReplacement(ctx, maxID, state, replaceCheckoutID, replaceVersion)
	}
	if replaceSlot {
		return p.requestReplacement(ctx, maxID, state, replaceSlotCheckoutID, replaceSlotVersion, selectedSlot)
	}
	if photoMessage {
		if state.Conversation != nil && state.Conversation.Flow == postReturnIssueFlow && state.Conversation.Step == "collect_photos" {
			return p.postReturnIssuePhotoStage(ctx, item, actor, maxID, *me.Employee, state)
		}
		if state.Conversation != nil && (state.Conversation.Flow == "issue_before" || state.Conversation.Flow == "issue_during" || state.Conversation.Flow == "issue_after") && state.Conversation.Step == "collect_photos" {
			return p.issuePhotoStage(ctx, item, actor, maxID, state)
		}
		if state.Return != nil && state.Trip != nil && state.Trip.Status == "returning" {
			return p.returnPhotoUpload(ctx, item, actor, maxID, *me.Employee, state)
		}
		return p.checkoutPhotoUpload(ctx, item, actor, maxID, state)
	}
	if catalog {
		if pageNumber == 0 {
			return p.sendView(ctx, maxID, "Кнопка списка устарела или повреждена. Обновите список.", [][]maxsdk.Button{{{Text: "Обновить", Payload: "cars:1"}}})
		}
		message, rows, err := p.catalogView(ctx, actor, *me.Employee, state, pageNumber)
		if err != nil {
			return err
		}
		return p.sendView(ctx, maxID, message, rows)
	}
	if card {
		message := "Некорректная ссылка на автомобиль. Откройте /cars."
		rows := [][]maxsdk.Button{{{Text: "К списку", Payload: "cars:1"}}}
		if vehicleIDPattern.MatchString(vehicleID) {
			vehicle, readErr := p.Data.Vehicle(ctx, actor, vehicleID)
			if readErr != nil {
				var apiErr *dataapi.APIError
				if !errors.As(readErr, &apiErr) || apiErr.Status != 404 {
					return readErr
				}
				message = "Автомобиль больше не доступен по этой ссылке. Обновите /cars."
			} else {
				message = cardText(vehicle, p.Location)
				message += "\n" + checkoutAvailabilityText(vehicle, *me.Employee, state)
				rows = append([][]maxsdk.Button{{{Text: "Предыдущий осмотр", Payload: "prev:" + vehicle.ID}}}, rows...)
				if vehicle.CurrentParking != nil {
					rows = append([][]maxsdk.Button{{{Text: "Показать на карте", URL: parkingMapURL(*vehicle.CurrentParking)}}}, rows...)
				}
				if canOfferCheckout(vehicle, *me.Employee, state) {
					rows = append([][]maxsdk.Button{{{Text: "Начать оформление", Payload: fmt.Sprintf("intent:%s:%d", vehicle.ID, vehicle.Version)}}}, rows...)
				}
				if expectedVersion > 0 && vehicle.Version != expectedVersion {
					message = "Данные автомобиля изменились. Ниже актуальная карточка.\n" + message
				}
			}
		}
		return p.sendView(ctx, maxID, message, rows)
	}
	if previous {
		message := "Некорректная ссылка на предыдущий осмотр. Откройте /cars."
		if vehicleIDPattern.MatchString(previousVehicleID) {
			inspection, readErr := p.Data.PreviousInspection(ctx, actor, previousVehicleID)
			if readErr != nil {
				var apiErr *dataapi.APIError
				if !errors.As(readErr, &apiErr) || apiErr.Status != 404 {
					return readErr
				}
				message = "Подтверждённого предыдущего осмотра пока нет."
			} else {
				message = previousInspectionText(inspection, p.Location)
			}
		}
		return p.sendView(ctx, maxID, message, [][]maxsdk.Button{{{Text: "К списку", Payload: "cars:1"}}})
	}
	message := menuText(*me.Employee, state)
	if state.Checkout != nil {
		message += "\nHold до: " + formatMoment(state.Checkout.ExpiresAt, p.Location)
		if state.Checkout.Status == "holding" && state.Checkout.Step == "inspection" {
			message += "\n" + photoProgress(state.Checkout.Inspection)
			if draft, ok := issuePhotoDraft(state); ok {
				message += fmt.Sprintf("\nДополнительные фото замечания: %d/3 (черновик).", len(draft.Context.AssetIDs))
			}
		}
	}
	if state.Conversation != nil && state.Conversation.Flow == "issue_before" && state.Conversation.Step == "done" && state.Conversation.Context.IssueID != nil {
		message += "\nЗамечание сохранено. Машина недоступна до проверки ответственного."
	}
	if state.Conversation != nil && state.Conversation.Flow == "issue_during" {
		if state.Conversation.Step == "done" && state.Conversation.Context.IssueID != nil {
			message += "\nПроблема во время поездки сохранена; следующая выдача заблокирована до проверки."
		} else if draft, ok := issuePhotoDraft(state); ok {
			message += fmt.Sprintf("\nПроблема во время поездки: черновик, %d/3 дополнительных фото; ещё не отправлена.", len(draft.Context.AssetIDs))
		}
	}
	if state.Conversation != nil && state.Conversation.Flow == "issue_after" && state.Return != nil && state.Conversation.Context.ReturnID != nil && *state.Conversation.Context.ReturnID == state.Return.ID {
		if state.Conversation.Step == "done" && state.Conversation.Context.IssueID != nil {
			message += "\nЗамечание при возврате сохранено."
		} else if state.Conversation.Step == "collect_photos" {
			message += fmt.Sprintf("\nПроблема при возврате: черновик, %d/3 дополнительных фото; ещё не отправлена.", len(state.Conversation.Context.AssetIDs))
		}
	}
	if state.Conversation != nil && state.Conversation.Flow == postReturnIssueFlow && state.Conversation.Context.TripID != nil {
		switch state.Conversation.Step {
		case "awaiting_description":
			message += "\nВыбрана проблема после поездки; отправьте описание командой /issue <описание>."
		case "collect_photos":
			message += fmt.Sprintf("\nЧерновик сообщения после поездки: %d/3 дополнительных фото; ещё не отправлен.", len(state.Conversation.Context.AssetIDs))
		case "done":
			message += "\nСообщение после поездки сохранено отдельно от завершённого осмотра."
		}
	}
	if state.Return != nil && state.Return.Step == "checklist" {
		if draft, _, _, ok := returnLocationConversation(state); ok && draft.Step == "confirm" {
			message += "\nГеопозиция получена, но место парковки ещё не подтверждено."
		} else if state.Return.ParkingLocation == nil {
			message += "\nОтправьте геопозицию для подтверждения места парковки."
		}
	}
	return p.sendView(ctx, maxID, message, menuRows(*me.Employee, state, p.MapBotName))
}

func answerTarget(event dataapi.NormalizedEvent) (string, int64, int, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "answer:") {
		return "", 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "answer:"), ":")
	if len(parts) != 3 {
		return "", 0, 0, true
	}
	version, versionErr := strconv.ParseInt(parts[1], 10, 64)
	option, optionErr := strconv.Atoi(parts[2])
	if versionErr != nil || optionErr != nil || version < 1 || option < 0 || option > 3 {
		return "", 0, 0, true
	}
	return parts[0], version, option, true
}

func acceptRulesTarget(event dataapi.NormalizedEvent) (string, int64, string, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil || !strings.HasPrefix(*event.Payload.CallbackData, "accept-rules:") {
		return "", 0, "", false
	}
	parts := strings.Split(strings.TrimPrefix(*event.Payload.CallbackData, "accept-rules:"), ":")
	if len(parts) != 3 {
		return "", 0, "", true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 {
		return "", 0, "", true
	}
	return parts[0], version, parts[2], true
}

func (p Bootstrap) createMath(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, checkoutID string, version int64) error {
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка вопроса повреждена. Откройте /menu.", nil)
	}
	checkout := state.Checkout
	if checkout == nil || checkout.ID != checkoutID || checkout.Status != "holding" || checkout.Step != "math" || checkout.Version != version {
		return p.sendView(ctx, maxID, "Шаг оформления изменился или hold истёк. Обновите /menu.", nil)
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, checkout.VehicleID)
	if err != nil {
		return err
	}
	if vehicle.Version < 2 || p.Commands == nil || item.LeaseToken == "" {
		return errors.New("math challenge requires a durable inbox lease and vehicle version")
	}
	key, err := inboxworker.CommandKey(item, "challenge.create")
	if err != nil {
		return err
	}
	result, err := p.Commands.ChallengeCreateTake(ctx, actor, checkout.ID, checkout.Version, checkout.VehicleID, vehicle.Version-1, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 409 {
			return p.sendView(ctx, maxID, "Вопрос устарел или hold истёк. Обновите /menu.", nil)
		}
		return err
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](result)
	if err != nil || !validTakeChallenge(challenge) {
		return errors.New("challenge create returned invalid aggregate")
	}
	return p.sendChallenge(ctx, maxID, challenge, "Выберите ответ. Попыток: 3.")
}

func (p Bootstrap) answerMath(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, challengeID string, version int64, option int) error {
	if !vehicleIDPattern.MatchString(challengeID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка ответа повреждена. Откройте /menu.", nil)
	}
	if state.Checkout == nil || state.Checkout.Status != "holding" || state.Checkout.Step != "math" {
		return p.sendView(ctx, maxID, "Шаг подтверждения изменился или hold истёк. Обновите /menu.", nil)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("challenge answer requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "challenge.answer")
	if err != nil {
		return err
	}
	result, err := p.Commands.ChallengeAnswer(ctx, actor, challengeID, version, option, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Ответ уже устарел или вопрос недоступен. Начните новый через /menu.", nil)
		}
		return err
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](result)
	if err != nil || !validTakeChallenge(challenge) || challenge.ID != challengeID || result.Correct == nil || result.AttemptsRemaining == nil || *result.AttemptsRemaining != challenge.AttemptsRemaining {
		return errors.New("challenge answer returned invalid aggregate")
	}
	if *result.Correct {
		return p.sendView(ctx, maxID, "Ответ верный. Следующий шаг — правила. Откройте /menu.", nil)
	}
	if *result.AttemptsRemaining == 0 {
		return p.sendView(ctx, maxID, "Три неверных ответа. Получите новый вопрос через /menu, пока hold действует.", nil)
	}
	return p.sendChallenge(ctx, maxID, challenge, fmt.Sprintf("Ответ неверный. Осталось попыток: %d.", *result.AttemptsRemaining))
}

func validTakeChallenge(challenge dataapi.Challenge) bool {
	return vehicleIDPattern.MatchString(challenge.ID) && challenge.Purpose == "take" && challenge.Question != "" && len(challenge.Options) == 4 && challenge.Version > 0 && challenge.AttemptsRemaining >= 0 && challenge.AttemptsRemaining <= 3 && !challenge.ExpiresAt.IsZero()
}

func (p Bootstrap) sendChallenge(ctx context.Context, maxID int64, challenge dataapi.Challenge, lead string) error {
	rows := make([][]maxsdk.Button, 0, 4)
	for index, value := range challenge.Options {
		rows = append(rows, []maxsdk.Button{{Text: strconv.Itoa(value), Payload: fmt.Sprintf("answer:%s:%d:%d", challenge.ID, challenge.Version, index)}})
	}
	text := lead + "\n" + oneLine(challenge.Question) + "\nВопрос до: " + formatMoment(challenge.ExpiresAt, p.Location)
	return p.sendView(ctx, maxID, text, rows)
}

func (p Bootstrap) checkoutRules(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, checkoutID string, version int64, shownRulesID string, accept bool) error {
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 || accept && !vehicleIDPattern.MatchString(shownRulesID) {
		return p.sendView(ctx, maxID, "Кнопка правил повреждена. Откройте /menu.", nil)
	}
	checkout := state.Checkout
	if checkout == nil || checkout.ID != checkoutID || checkout.Version != version || checkout.Status != "holding" || checkout.Step != "rules" || checkout.IntentConfirmedAt == nil || checkout.RulesAcceptedAt != nil {
		return p.sendView(ctx, maxID, "Шаг правил изменился или hold истёк. Обновите /menu.", nil)
	}
	rules, err := p.Data.CurrentRules(ctx, actor)
	if err != nil {
		return err
	}
	if !vehicleIDPattern.MatchString(rules.ID) || strings.TrimSpace(rules.Body) == "" || strings.TrimSpace(rules.VersionLabel) == "" || utf8.RuneCountInString(rules.Body) > 10000 || utf8.RuneCountInString(rules.VersionLabel) > 50 {
		return errors.New("current rules are invalid")
	}
	if !accept {
		return p.sendRules(ctx, maxID, *checkout, rules, "")
	}
	if shownRulesID != rules.ID {
		return p.sendRules(ctx, maxID, *checkout, rules, "Правила изменились. Прочитайте текущую версию.\n")
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("rules acceptance requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "checkout.accept_rules")
	if err != nil {
		return err
	}
	result, err := p.Commands.CheckoutAcceptRules(ctx, actor, checkout.ID, checkout.Version, rules.ID, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 409 || apiErr.Status == 404) {
			return p.sendView(ctx, maxID, "Правила или оформление изменились. Обновите /menu и прочитайте текущую версию.", nil)
		}
		return err
	}
	accepted, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil || accepted.ID != checkout.ID || accepted.Status != "holding" || accepted.Step != "inspection" || accepted.RulesVersionID == nil || *accepted.RulesVersionID != rules.ID || accepted.RulesAcceptedAt == nil {
		return errors.New("rules acceptance returned invalid aggregate")
	}
	return p.sendView(ctx, maxID, "Правила версии "+oneLine(rules.VersionLabel)+" приняты. Осмотр до поездки: нужно 8 фотографий. Откройте /menu для продолжения.", nil)
}

func (p Bootstrap) sendRules(ctx context.Context, maxID int64, checkout dataapi.Checkout, rules dataapi.Rules, lead string) error {
	const chunkSize = 3500 // Leaves space for version and part labels under MAX's 4000-rune limit.
	body := []rune(rules.Body)
	parts := (len(body) + chunkSize - 1) / chunkSize
	rows := [][]maxsdk.Button{{{Text: "Принимаю правила", Payload: fmt.Sprintf("accept-rules:%s:%d:%s", checkout.ID, checkout.Version, rules.ID)}}, {{Text: "Отменить оформление", Payload: fmt.Sprintf("cancel-intent:%s:%d", checkout.ID, checkout.Version)}}}
	for part := 0; part < parts; part++ {
		end := min(len(body), (part+1)*chunkSize)
		text := lead + fmt.Sprintf("Правила (версия %s, часть %d/%d):\n", oneLine(rules.VersionLabel), part+1, parts) + string(body[part*chunkSize:end])
		if part == parts-1 {
			return p.sendView(ctx, maxID, text, rows)
		}
		if err := p.sendView(ctx, maxID, text, nil); err != nil {
			return err
		}
	}
	return errors.New("rules text is empty")
}

func (p Bootstrap) cancelCheckout(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, state dataapi.CurrentState, checkoutID string, version int64, confirm bool) error {
	if !vehicleIDPattern.MatchString(checkoutID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка отмены повреждена. Откройте /menu.", nil)
	}
	checkout := state.Checkout
	if checkout == nil || checkout.ID != checkoutID {
		return p.sendView(ctx, maxID, "Активное оформление не найдено. Откройте /menu.", nil)
	}
	if checkout.Version != version || checkout.Status != "holding" {
		return p.sendView(ctx, maxID, "Оформление изменилось. Обновите /menu.", nil)
	}
	if !confirm {
		rows := [][]maxsdk.Button{{{Text: "Да, отменить", Payload: fmt.Sprintf("cancel:%s:%d", checkout.ID, checkout.Version)}}, {{Text: "Нет, оставить", Payload: "menu"}}}
		return p.sendView(ctx, maxID, "Отменить оформление и освободить машину? Поездка не начата.", rows)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("checkout cancellation requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "checkout.cancel")
	if err != nil {
		return err
	}
	result, err := p.Commands.CheckoutCancel(ctx, actor, checkout.ID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 409 && (apiErr.Code == "HOLD_EXPIRED" || apiErr.Code == "STALE_VERSION" || apiErr.Code == "INVALID_STATE") {
			return p.sendView(ctx, maxID, "Оформление уже изменилось или истекло. Обновите /menu.", nil)
		}
		return err
	}
	cancelled, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil || cancelled.ID != checkout.ID || cancelled.Status != "cancelled" {
		return errors.New("checkout cancellation returned invalid aggregate")
	}
	return p.sendView(ctx, maxID, "Оформление отменено. Машина освобождена. Откройте /cars.", [][]maxsdk.Button{{{Text: "Доступные автомобили", Payload: "cars:1"}}})
}

func parkingMapURL(parking dataapi.ParkingLocation) string {
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f", parking.Latitude, parking.Longitude, parking.Latitude, parking.Longitude)
}

func vehicleActionTarget(event dataapi.NormalizedEvent, prefix string) (string, int64, bool) {
	if event.EventType != "message_callback" || event.Payload.Kind != "callback" || event.Payload.CallbackData == nil {
		return "", 0, false
	}
	payload := *event.Payload.CallbackData
	if !strings.HasPrefix(payload, prefix) {
		return "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(payload, prefix), ":")
	if len(parts) != 2 {
		return "", 0, true
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version < 1 {
		return "", 0, true
	}
	return parts[0], version, true
}

func (p Bootstrap) checkoutIntent(ctx context.Context, item dataapi.InboxClaimItem, actor string, maxID int64, employee dataapi.Employee, state dataapi.CurrentState, vehicleID string, version int64, confirm bool) error {
	refresh := [][]maxsdk.Button{{{Text: "Обновить список", Payload: "cars:1"}}}
	if !vehicleIDPattern.MatchString(vehicleID) || version < 1 {
		return p.sendView(ctx, maxID, "Кнопка оформления повреждена. Обновите список.", refresh)
	}
	if state.Checkout != nil {
		if state.Checkout.VehicleID == vehicleID {
			return p.sendView(ctx, maxID, "Оформление уже начато. Hold до "+formatMoment(state.Checkout.ExpiresAt, p.Location)+". Откройте /menu для продолжения.", nil)
		}
		return p.sendView(ctx, maxID, "Сначала завершите текущее оформление. Откройте /menu.", nil)
	}
	if state.Trip != nil || !employee.CanStartTrip {
		return p.sendView(ctx, maxID, "Сейчас нельзя начать оформление автомобиля. Откройте /menu.", nil)
	}
	vehicle, err := p.Data.Vehicle(ctx, actor, vehicleID)
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return p.sendView(ctx, maxID, "Автомобиль больше не доступен. Обновите список.", refresh)
		}
		return err
	}
	if vehicle.Version != version || !canOfferCheckout(vehicle, employee, state) {
		return p.sendView(ctx, maxID, "Данные автомобиля изменились или выдача недоступна. Обновите список.", refresh)
	}
	if !confirm {
		text := fmt.Sprintf("Подтвердите оформление %s. После подтверждения машина резервируется на 15 минут; поездка ещё не начнётся.", oneLine(vehicle.Plate))
		rows := [][]maxsdk.Button{{{Text: "Подтвердить", Payload: fmt.Sprintf("take:%s:%d", vehicle.ID, vehicle.Version)}}, {{Text: "Назад к карточке", Payload: fmt.Sprintf("car:%s:%d", vehicle.ID, vehicle.Version)}}}
		return p.sendView(ctx, maxID, text, rows)
	}
	if p.Commands == nil || item.LeaseToken == "" {
		return errors.New("checkout command requires a durable inbox lease")
	}
	key, err := inboxworker.CommandKey(item, "checkout.create")
	if err != nil {
		return err
	}
	result, err := p.Commands.CheckoutCreate(ctx, actor, vehicle.ID, version, key, &dataapi.InboxLease{EventID: item.ID, Token: item.LeaseToken})
	if err != nil {
		var apiErr *dataapi.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 409 && (apiErr.Code == "STALE_VERSION" || apiErr.Code == "VEHICLE_UNAVAILABLE" || apiErr.Code == "USER_BUSY") {
			return p.sendView(ctx, maxID, "Машина уже недоступна или оформление изменилось. Обновите /menu и список.", refresh)
		}
		return err
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil || checkout.ID == "" || checkout.VehicleID != vehicle.ID || checkout.ExpiresAt.IsZero() {
		return errors.New("checkout command returned invalid aggregate")
	}
	return p.sendView(ctx, maxID, "Машина зарезервирована до "+formatMoment(checkout.ExpiresAt, p.Location)+". Поездка ещё не началась. Откройте /menu для продолжения оформления.", nil)
}

func canOfferCheckout(vehicle dataapi.Vehicle, employee dataapi.Employee, state dataapi.CurrentState) bool {
	return employee.CanStartTrip && state.Trip == nil && state.Checkout == nil && vehicle.Status == "available" && !vehicle.ManualBlocked && !vehicle.NeedsReview && vehicle.CurrentParking != nil && !vehicle.CurrentParking.ConfirmedAt.IsZero() && strings.TrimSpace(vehicle.KeyInstructions) != ""
}

func checkoutAvailabilityText(vehicle dataapi.Vehicle, employee dataapi.Employee, state dataapi.CurrentState) string {
	if !employee.CanStartTrip || state.Trip != nil || state.Checkout != nil {
		return "Выдача: недоступна для текущего пользователя или пока не завершён текущий сценарий."
	}
	if vehicle.Status != "available" || vehicle.ManualBlocked || vehicle.NeedsReview {
		return "Выдача: автомобиль сейчас недоступен. Обновите список."
	}
	if !canOfferCheckout(vehicle, employee, state) {
		return "Выдача: требуется подтверждённая парковка и инструкция по ключам."
	}
	return "Выдача: доступно подтверждение оформления; поездка начнётся только после приёмки."
}

func previousTarget(event dataapi.NormalizedEvent) (string, bool) {
	if event.EventType == "message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil {
		payload := *event.Payload.CallbackData
		if strings.HasPrefix(payload, "prev:") {
			return strings.TrimPrefix(payload, "prev:"), true
		}
	}
	return "", false
}

func previousInspectionText(inspection dataapi.Inspection, location *time.Location) string {
	if inspection.Phase != "after" || inspection.Status != "finalized" {
		return "Подтверждённого предыдущего осмотра пока нет."
	}
	lines := []string{"Предыдущий завершённый осмотр", "Состояние на " + formatMoment(inspection.UpdatedAt, location)}
	if inspection.FuelLevel == nil {
		lines = append(lines, "Топливо: Не указано")
	} else {
		lines = append(lines, fmt.Sprintf("Топливо: %d%%", *inspection.FuelLevel))
	}
	if inspection.OdometerKM == nil {
		lines = append(lines, "Пробег: Не указано")
	} else {
		lines = append(lines, fmt.Sprintf("Пробег: %d км", *inspection.OdometerKM))
	}
	lines = append(lines, fmt.Sprintf("Фото: %d из 8", len(inspection.OccupiedSlots)))
	return strings.Join(lines, "\n")
}

func (p Bootstrap) sendView(ctx context.Context, maxID int64, text string, rows [][]maxsdk.Button) error {
	if len(rows) == 0 {
		_, err := p.MAX.SendText(ctx, maxID, text)
		return err
	}
	_, err := p.MAX.SendButtons(ctx, maxID, text, rows)
	return err
}

func cardTarget(event dataapi.NormalizedEvent) (string, int64, bool) {
	if event.EventType == "message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil {
		payload := *event.Payload.CallbackData
		if !strings.HasPrefix(payload, "car:") {
			return "", 0, false
		}
		parts := strings.Split(payload, ":")
		if len(parts) != 3 {
			return "", 0, true
		}
		version, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || version < 1 {
			return "", 0, true
		}
		return parts[1], version, true
	}
	if event.EventType != "message_created" || event.Payload.Kind != "text" || event.Payload.Text == nil {
		return "", 0, false
	}
	command := strings.TrimSpace(*event.Payload.Text)
	if !strings.HasPrefix(command, "/car ") {
		return "", 0, false
	}
	return strings.TrimSpace(strings.TrimPrefix(command, "/car ")), 0, true
}

func cardText(vehicle dataapi.Vehicle, location *time.Location) string {
	lines := []string{
		fmt.Sprintf("%s · %s %s", oneLine(vehicle.Plate), oneLine(vehicle.Make), oneLine(vehicle.Model)),
		"Статус: " + oneLine(vehicle.Status),
	}
	if vehicle.CurrentParking == nil {
		lines = append(lines, "Место парковки: Не указано")
	} else {
		parking := vehicle.CurrentParking
		lines = append(lines, fmt.Sprintf("Место парковки: %.6f, %.6f", parking.Latitude, parking.Longitude))
		if parking.Landmark != nil && strings.TrimSpace(*parking.Landmark) != "" {
			lines = append(lines, "Ориентир: "+oneLine(*parking.Landmark))
		}
		lines = append(lines, "Место подтверждено: "+formatMoment(parking.ConfirmedAt, location))
	}
	if vehicle.CurrentFuel == nil {
		lines = append(lines, "Топливо: Не указано")
	} else {
		lines = append(lines, fmt.Sprintf("Топливо: %d%%", *vehicle.CurrentFuel))
	}
	if vehicle.FuelConfirmedAt != nil {
		lines = append(lines, "Топливо обновлено: "+formatMoment(*vehicle.FuelConfirmedAt, location))
	}
	if vehicle.CurrentOdometerKM == nil {
		lines = append(lines, "Пробег: Не указано")
	} else {
		lines = append(lines, fmt.Sprintf("Пробег: %d км", *vehicle.CurrentOdometerKM))
	}
	if vehicle.OdometerConfirmedAt != nil {
		lines = append(lines, "Пробег обновлён: "+formatMoment(*vehicle.OdometerConfirmedAt, location))
	}
	lines = append(lines, "Описание: "+valueOrUnknown(vehicle.Description))
	if len(vehicle.KnownNonblockingIssues) == 0 {
		lines = append(lines, "Известные замечания: Нет")
	} else {
		issues := make([]string, 0, len(vehicle.KnownNonblockingIssues))
		for _, issue := range vehicle.KnownNonblockingIssues {
			issues = append(issues, oneLine(issue))
		}
		lines = append(lines, "Известные замечания: "+strings.Join(issues, "; "))
	}
	lines = append(lines, "Ключи: "+valueOrUnknown(vehicle.KeyInstructions), "К списку: /cars")
	return strings.Join(lines, "\n")
}

func valueOrUnknown(value string) string {
	value = oneLine(value)
	if value == "" {
		return "Не указано"
	}
	return value
}

func formatMoment(moment time.Time, location *time.Location) string {
	if moment.IsZero() {
		return "Не указано"
	}
	if location == nil {
		location = time.UTC
	}
	return moment.In(location).Format("02.01.2006 15:04 MST")
}

func catalogPage(event dataapi.NormalizedEvent) (int, bool) {
	if event.EventType == "message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil {
		payload := *event.Payload.CallbackData
		if !strings.HasPrefix(payload, "cars:") {
			return 0, false
		}
		page, err := strconv.Atoi(strings.TrimPrefix(payload, "cars:"))
		if err != nil || page < 1 || page > 20 {
			return 0, true
		}
		return page, true
	}
	if event.EventType != "message_created" || event.Payload.Kind != "text" || event.Payload.Text == nil {
		return 0, false
	}
	command := strings.ToLower(strings.TrimSpace(*event.Payload.Text))
	if command == "доступные автомобили" || command == "/cars" {
		return 1, true
	}
	if !strings.HasPrefix(command, "/cars ") {
		return 0, false
	}
	page, err := strconv.Atoi(strings.TrimPrefix(command, "/cars "))
	if err != nil || page < 1 || page > 20 {
		return 0, true
	}
	return page, true
}

func (p Bootstrap) catalogView(ctx context.Context, actor string, employee dataapi.Employee, state dataapi.CurrentState, wanted int) (string, [][]maxsdk.Button, error) {
	if !employee.CanStartTrip || state.Trip != nil || state.Checkout != nil {
		return "Сейчас нельзя начать оформление другой машины. Откройте /menu, чтобы продолжить текущий сценарий.", [][]maxsdk.Button{{{Text: "В меню", Payload: "menu"}}}, nil
	}
	available := true
	cursor := ""
	var page dataapi.Page[dataapi.Vehicle]
	for number := 1; number <= wanted; number++ {
		var err error
		page, err = p.Data.Vehicles(ctx, actor, dataapi.VehicleFilter{Available: &available, Limit: 5, Cursor: cursor})
		if err != nil {
			return "", nil, err
		}
		if number < wanted {
			if page.NextCursor == nil {
				return "Список изменился. Обновите: /cars", [][]maxsdk.Button{{{Text: "Обновить", Payload: "cars:1"}}}, nil
			}
			cursor = *page.NextCursor
		}
	}
	if len(page.Items) == 0 {
		return "Сейчас нет доступных автомобилей. Попробуйте обновить список позже.", [][]maxsdk.Button{{{Text: "Обновить", Payload: "cars:1"}}}, nil
	}
	lines := []string{fmt.Sprintf("Доступные автомобили · страница %d", wanted)}
	rows := make([][]maxsdk.Button, 0, len(page.Items)+1)
	for _, vehicle := range page.Items {
		label := fmt.Sprintf("%s · %s %s", oneLine(vehicle.Plate), oneLine(vehicle.Make), oneLine(vehicle.Model))
		lines = append(lines, label)
		rows = append(rows, []maxsdk.Button{{Text: shortLabel(label), Payload: fmt.Sprintf("car:%s:%d", vehicle.ID, vehicle.Version)}})
	}
	controls := []maxsdk.Button{{Text: "Обновить", Payload: "cars:1"}}
	if wanted > 1 {
		lines = append(lines, fmt.Sprintf("Назад: /cars %d", wanted-1))
		controls = append(controls, maxsdk.Button{Text: "Назад", Payload: fmt.Sprintf("cars:%d", wanted-1)})
	}
	if page.NextCursor != nil && wanted < 20 {
		lines = append(lines, fmt.Sprintf("Далее: /cars %d", wanted+1))
		controls = append(controls, maxsdk.Button{Text: "Далее", Payload: fmt.Sprintf("cars:%d", wanted+1)})
	}
	lines = append(lines, "Обновить: /cars")
	rows = append(rows, controls)
	return strings.Join(lines, "\n"), rows, nil
}

func shortLabel(value string) string {
	runes := []rune(value)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return value
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func isMenuEvent(event dataapi.NormalizedEvent) bool {
	if event.EventType == "message_callback" && event.Payload.Kind == "callback" && event.Payload.CallbackData != nil {
		return *event.Payload.CallbackData == "menu"
	}
	if event.EventType == "bot_started" && event.Payload.Kind == "start" {
		return true
	}
	if event.EventType != "message_created" || event.Payload.Kind != "text" || event.Payload.Text == nil {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(*event.Payload.Text))
	return command == "/start" || command == "/menu"
}

func menuRows(employee dataapi.Employee, state dataapi.CurrentState, mapBotName ...string) [][]maxsdk.Button {
	if state.Checkout != nil && state.Checkout.Status == "holding" {
		rows := [][]maxsdk.Button{}
		if state.Checkout.Step == "math" {
			rows = append(rows, []maxsdk.Button{{Text: "Продолжить оформление", Payload: fmt.Sprintf("math:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
		}
		if state.Checkout.Step == "rules" {
			rows = append(rows, []maxsdk.Button{{Text: "Прочитать правила", Payload: fmt.Sprintf("rules:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
		}
		if state.Checkout.Step == "inspection" {
			rows = append(rows, []maxsdk.Button{{Text: "Продолжить фото", Payload: fmt.Sprintf("photos:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
			if slot, count, ok := photoSlot(state.Checkout.Inspection); ok {
				if slot == 0 && state.Checkout.Inspection.PhotosConfirmedAt == nil {
					rows = append(rows, []maxsdk.Button{{Text: "Подтвердить фотографии", Payload: fmt.Sprintf("confirm-photos:%s:%d", state.Checkout.Inspection.ID, state.Checkout.Inspection.Version)}})
				}
				if count > 0 {
					rows = append(rows, []maxsdk.Button{{Text: "Заменить фотографию", Payload: fmt.Sprintf("replace-photos:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
				}
			}
			rows = append(rows, []maxsdk.Button{{Text: "Указать топливо", Payload: fmt.Sprintf("fuel:%s:%d", state.Checkout.Inspection.ID, state.Checkout.Inspection.Version)}})
			rows = append(rows, []maxsdk.Button{{Text: "Указать пробег", Payload: fmt.Sprintf("odometer:%s:%d", state.Checkout.Inspection.ID, state.Checkout.Inspection.Version)}})
			if inspectionReadyForIssueQuestion(state.Checkout.Inspection) {
				if state.Checkout.Inspection.NewDamage != nil && *state.Checkout.Inspection.NewDamage {
					rows = append(rows, []maxsdk.Button{{Text: "Описать замечание", Payload: fmt.Sprintf("issue-draft:%s:%d", state.Checkout.Inspection.ID, state.Checkout.Inspection.Version)}})
				} else if state.Checkout.NoNewIssues == nil || !*state.Checkout.NoNewIssues {
					rows = append(rows, []maxsdk.Button{{Text: "Новые замечания", Payload: fmt.Sprintf("new-issues:%s:%d", state.Checkout.Inspection.ID, state.Checkout.Inspection.Version)}})
				}
			}
			if draft, ok := issuePhotoDraft(state); ok {
				rows = append(rows, []maxsdk.Button{{Text: "Фото замечания", Payload: fmt.Sprintf("issue-photos:%s:%d", state.Checkout.Inspection.ID, draft.Version)}})
				rows = append(rows, []maxsdk.Button{{Text: "Проверить замечание", Payload: fmt.Sprintf("issue-review:%s:%d", state.Checkout.Inspection.ID, draft.Version)}})
			}
			if readyForCheckoutStart(*state.Checkout) {
				rows = append(rows, []maxsdk.Button{{Text: "Проверить итог перед выездом", Payload: fmt.Sprintf("checkout-summary:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
			}
		}
		return append(rows, []maxsdk.Button{{Text: "Отменить оформление", Payload: fmt.Sprintf("cancel-intent:%s:%d", state.Checkout.ID, state.Checkout.Version)}})
	}
	rows := [][]maxsdk.Button{{{Text: "Мои поездки", Payload: "trip-list:mine:1"}}}
	if state.Return != nil && state.Trip != nil && state.Trip.Status == "returning" {
		if state.Return.Step == "math" {
			rows = append([][]maxsdk.Button{{{Text: "Продолжить возврат · проверка", Payload: fmt.Sprintf("return-math:%s:%d", state.Return.ID, state.Return.Version)}}}, rows...)
		}
		if state.Return.Step == "checklist" && nextReturnCheckField(state.Return.Inspection) != "" {
			rows = append([][]maxsdk.Button{{{Text: "Продолжить анкету возврата", Payload: fmt.Sprintf("return-check:%s:%d", state.Return.Inspection.ID, state.Return.Inspection.Version)}}}, rows...)
		}
		if state.Return.Step == "checklist" {
			inspection := state.Return.Inspection
			if nextReturnCheckField(inspection) == "" && len(inspection.OccupiedSlots) == 8 && inspection.PhotosConfirmedAt != nil && inspection.FuelLevel != nil && inspection.OdometerKM != nil {
				rows = append(rows, []maxsdk.Button{{Text: "Проверить итог возврата", Payload: fmt.Sprintf("return-summary:%s:%d", state.Return.ID, state.Return.Version)}})
			}
			if draft, _, _, ok := returnLocationConversation(state); ok && draft.Step == "confirm" {
				rows = append(rows, []maxsdk.Button{{Text: "Подтвердить геопозицию", Payload: fmt.Sprintf("return-geo-confirm:%s:%d", state.Return.ID, draft.Version)}})
			}
			draft, issueOpen := issuePhotoDraft(state)
			if !issueOpen || draft.Flow != "issue_after" {
				rows = append(rows, []maxsdk.Button{{Text: "Сообщить проблему при возврате", Payload: fmt.Sprintf("return-issue:%s:%d", state.Return.ID, state.Return.Version)}})
			}
			if issueOpen && draft.Flow == "issue_after" {
				rows = append(rows, []maxsdk.Button{{Text: "Фото проблемы при возврате", Payload: fmt.Sprintf("return-issue-photos:%s:%d", state.Return.Inspection.ID, draft.Version)}})
				rows = append(rows, []maxsdk.Button{{Text: "Проверить проблему при возврате", Payload: fmt.Sprintf("return-issue-review:%s:%d", state.Return.Inspection.ID, draft.Version)}})
			}
			rows = append(rows, []maxsdk.Button{{Text: "Фото после поездки", Payload: fmt.Sprintf("return-photos:%s:%d", state.Return.ID, state.Return.Version)}})
			rows = append(rows, []maxsdk.Button{{Text: "Топливо при возврате", Payload: fmt.Sprintf("return-fuel:%s:%d", state.Return.Inspection.ID, state.Return.Inspection.Version)}})
			rows = append(rows, []maxsdk.Button{{Text: "Пробег при возврате", Payload: fmt.Sprintf("return-odometer:%s:%d", state.Return.Inspection.ID, state.Return.Inspection.Version)}})
			if len(state.Return.Inspection.OccupiedSlots) > 0 {
				rows = append(rows, []maxsdk.Button{{Text: "Заменить фото после", Payload: fmt.Sprintf("return-replace:%s:%d", state.Return.ID, state.Return.Version)}})
			}
			if slot, _, ok := photoSlotPhase(state.Return.Inspection, "after"); ok && slot == 0 && state.Return.Inspection.PhotosConfirmedAt == nil {
				rows = append(rows, []maxsdk.Button{{Text: "Подтвердить фото после", Payload: fmt.Sprintf("return-confirm-photos:%s:%d", state.Return.Inspection.ID, state.Return.Inspection.Version)}})
			}
		}
		tripRow := []maxsdk.Button{{Text: "Текущая поездка", Payload: "trip:" + state.Trip.ID}}
		if state.Return.Step == "checklist" && state.Return.Status == "draft" && state.Return.IntentConfirmedAt != nil &&
			state.Return.TripID == state.Trip.ID && state.Trip.EmployeeID == employee.ID && state.Trip.ReturnID != nil && *state.Trip.ReturnID == state.Return.ID && len(mapBotName) > 0 {
			if mapURL := mapLaunchURL(mapBotName[0], state.Return.ID); mapURL != "" {
				tripRow = append(tripRow, maxsdk.Button{Text: "Выбрать на карте", URL: mapURL})
			}
		}
		rows = append(rows, tripRow)
		rows = append(rows, []maxsdk.Button{{Text: "Вернуться к поездке", Payload: fmt.Sprintf("return-cancel-intent:%s:%d", state.Return.ID, state.Return.Version)}})
	}
	if state.Trip != nil && state.Trip.Status == "active" {
		rows = append([][]maxsdk.Button{{{Text: "Текущая поездка", Payload: "trip:" + state.Trip.ID}}}, rows...)
		if draft, ok := issuePhotoDraft(state); ok && draft.Flow == "issue_during" {
			rows = append(rows, []maxsdk.Button{{Text: "Фото проблемы", Payload: fmt.Sprintf("trip-issue-photos:%s:%d", state.Trip.ID, draft.Version)}})
			rows = append(rows, []maxsdk.Button{{Text: "Проверить проблему", Payload: fmt.Sprintf("trip-issue-review:%s:%d", state.Trip.ID, draft.Version)}})
		}
	}
	if state.Conversation != nil && state.Conversation.Flow == postReturnIssueFlow && state.Conversation.Context.TripID != nil {
		draft := state.Conversation
		switch draft.Step {
		case "collect_photos":
			rows = append(rows, []maxsdk.Button{{Text: "Фото к сообщению после поездки", Payload: fmt.Sprintf("trip-post-issue-photos:%s:%d", *draft.Context.TripID, draft.Version)}})
			rows = append(rows, []maxsdk.Button{{Text: "Проверить сообщение после поездки", Payload: fmt.Sprintf("trip-post-issue-review:%s:%d", *draft.Context.TripID, draft.Version)}})
		case "done":
			rows = append(rows, []maxsdk.Button{{Text: "Открыть завершённую поездку", Payload: "trip:" + *draft.Context.TripID}})
		}
	}
	if employee.CanStartTrip && state.Trip == nil && state.Checkout == nil {
		rows = append([][]maxsdk.Button{{{Text: "Доступные автомобили", Payload: "cars:1"}}}, rows...)
	}
	if employee.Role == "admin" {
		adminButton := maxsdk.Button{Text: "Поездки автопарка", Payload: "trip-list:admin:1"}
		if len(rows) >= 10 {
			for index := range rows {
				if len(rows[index]) == 1 && rows[index][0].Payload == "trip-list:mine:1" {
					rows[index] = append(rows[index], adminButton)
					break
				}
			}
		} else {
			rows = append(rows, []maxsdk.Button{adminButton})
		}
	}
	return rows
}

func menuText(employee dataapi.Employee, state dataapi.CurrentState) string {
	lines := []string{"MAX Fleet"}
	if state.Trip != nil {
		if state.Return != nil {
			lines = append(lines, "Продолжить возврат")
		} else {
			lines = append(lines, "Текущая поездка")
		}
	} else {
		if state.Checkout != nil {
			lines = append(lines, "Продолжить оформление")
		} else if employee.CanStartTrip {
			lines = append(lines, "Доступные автомобили")
		}
	}
	lines = append(lines, "Мои поездки", "Правила и помощь")
	if employee.Role == "admin" {
		lines = append(lines, "Управление автопарком")
	}
	return strings.Join(lines, "\n")
}
