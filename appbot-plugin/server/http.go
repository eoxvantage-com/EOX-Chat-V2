package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	// import sql drivers
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"

	"github.com/mattermost/mattermost-server/v5/model"
	"github.com/mattermost/mattermost-server/v5/plugin"
	"github.com/pkg/errors"
)

type Payload struct {
	BotName     string `json:"botName"`
	MessageBody string `json:"message"`
	MessageFrom string `json:"from"`
	MessagesTo  string `json:"toList"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	UrlLink     string `json:"url"`
	CommentId   string `json:"commentId"`
	FileIds     string `json:"fileIds"`
}

type Data struct {
	FileId string `json:"FileId"`
	Uuid   string `json:"uuid"`
}

type Check struct {
	Status string `json:"status"`
	Data   Data   `json:"data"`
}

type CommentAttachments struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Thumbnail string `json:"thumbnail"`
	Preview   string `json:"preview"`
}

const (
	eoxAPIUrl = "http://api-service/"
)

func (p *Plugin) InitAPI() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/dialog", p.handleDialog).Methods("POST")

	r.HandleFunc("/view/ephemeral", p.handleViewEphemeral).Methods("POST")
	r.HandleFunc("/view/complete/list", p.handleViewCompleteList).Methods("POST")

	r.HandleFunc("/complete", p.handleComplete).Methods("POST")
	r.HandleFunc("/complete/list", p.handleCompleteList).Methods("POST")

	r.HandleFunc("/delete", p.handleDelete).Methods("POST")
	r.HandleFunc("/delete/ephemeral", p.handleDeleteEphemeral).Methods("POST")
	r.HandleFunc("/delete/list", p.handleDeleteList).Methods("POST")
	r.HandleFunc("/delete/complete/list", p.handleDeleteCompleteList).Methods("POST")

	r.HandleFunc("/snooze", p.handleSnooze).Methods("POST")
	r.HandleFunc("/snooze/list", p.handleSnoozeList).Methods("POST")

	r.HandleFunc("/close/list", p.handleCloseList).Methods("POST")

	r.HandleFunc("/next/reminders", p.handleNextReminders).Methods("POST")

	r.HandleFunc("/appbot", p.handleAppBot).Methods("POST")

	return r
}

func (p *Plugin) ServeHTTP(c *plugin.Context, w http.ResponseWriter, r *http.Request) {
	p.router.ServeHTTP(w, r)
}

func checkCount(rows *sql.Rows) (count int) {
	for rows.Next() {
		err := rows.Scan(&count)
		if err != nil {
			panic(err)
		}
	}
	return count
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

func (p *Plugin) handleAppBot(w http.ResponseWriter, r *http.Request) {
	fmt.Println("handleAppBothandleAppBot TO")
	fmt.Println(r.Body)
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s", b)

	config := p.API.GetUnsanitizedConfig()

	var l Payload
	// errNew := json.NewDecoder(r.Body).Decode(&l)
	errNew := json.Unmarshal([]byte(b), &l)
	if errNew != nil {
		panic(errNew)
	}
	fmt.Println("payloadddd SSS:")
	fmt.Println(l.MessageFrom)
	db, err := setupConnection(*config.SqlSettings.DataSource, config.SqlSettings)
	if err != nil {
		p.API.LogError("failed to connect to master db" + err.Error())
		return
	}
	_, checkTableExists := db.Query(`SELECT table_name
FROM information_schema.tables
WHERE table_name LIKE 'postfilehistory';`)

	if checkTableExists != nil {
		_, tableCreationErr := db.Exec(`CREATE TABLE IF NOT EXISTS PostFileHistory (
            Id SERIAL PRIMARY KEY,
            PostId VARCHAR(26) NOT NULL,
            FileId VARCHAR(200) NOT NULL,
            UserId VARCHAR(26) NOT NULL,
            ChannelId VARCHAR(26) NOT NULL,
            BotUserName VARCHAR(64) NOT NULL
    )`)
		if tableCreationErr != nil {
			p.API.LogError("failed to create PostFileHistory table" + tableCreationErr.Error())
			return
		}
	} else {
		checkColumnExists, _ := db.Query(`SELECT COUNT(*) AS count
									FROM information_schema.columns
									WHERE table_name = 'PostFileHistory'
									AND column_name = 'Id';
								`)
		if checkCount(checkColumnExists) == 0 {
			_, err = db.Exec(`ALTER TABLE PostFileHistory DROP PRIMARY KEY`)
			_, err = db.Exec(`ALTER TABLE PostFileHistory ADD Id INT(20) PRIMARY KEY AUTO_INCREMENT`)
		}
	}

	_, commentTableCreationErr := db.Exec(`CREATE TABLE IF NOT EXISTS PostCommentHistory (
			PostFileHistoryId INT NOT NULL,
			PostId VARCHAR(26) NOT NULL,
			CommentId VARCHAR(200) NOT NULL,
			ParentPostId VARCHAR(26),
			CONSTRAINT pk_PostCommentHistory PRIMARY KEY (PostFileHistoryId, CommentId)
		)`)
	_, err = db.Exec(`ALTER TABLE PostFileHistory ADD FOREIGN KEY (PostFileHistoryId) REFERENCES PostFileHistory(Id)`)
	_, err = db.Exec(`ALTER TABLE PostFileHistory ADD FOREIGN KEY (ParentPostId) REFERENCES PostCommentHistory(PostId)`)
	if commentTableCreationErr != nil {
		p.API.LogError("failed to create PostCommentHistory table" + commentTableCreationErr.Error())
		return
	}

	fromUser, fErr := p.API.GetUserByUsername(l.MessageFrom)
	fmt.Fprint(w, fromUser)
	if fErr != nil {
		p.API.LogError("failed to find from user " + l.MessageFrom)
		return
	}
	var botUserId, finalMessageBody string

	botUser, bErr := p.API.GetUserByUsername(l.BotName)
	fmt.Fprint(w, botUser)
	if bErr != nil {
		p.API.LogError("failed to find target user " + l.BotName)
		bot := &model.Bot{
			Username:    l.BotName,
			DisplayName: l.BotName,
		}
		options := []plugin.EnsureBotOption{
			plugin.ProfileImagePath("assets/icon.png"),
		}

		botUserId, _ = p.Helpers.EnsureBot(bot, options...)

	} else {
		botUserId = botUser.Id
	}
	finalMessageBody = l.MessageBody
	var newarray []string

	// MessageToList Convertion from string to array with @ prepend
	fmt.Println("MESSAGE TO------")
	fmt.Println(l.MessagesTo)
	toList := strings.Split(l.MessagesTo, ",")
	for j := range toList {
		chk := "@" + toList[j]
		newarray = append(newarray, chk)
	}
	if strings.Contains(l.MessageBody, "@") {
		uff := strings.Trim(strings.Join(strings.Fields(fmt.Sprint(newarray)), " "), "[]")
		fmt.Println(uff)
		re := regexp.MustCompile("@(\\w+)?") // Reg Expression to match @.*
		fmt.Println(re)
		test := re.FindAllString(l.MessageBody, -1) // Check regExp in the MessageBody
		fmt.Println("----FINAL TO LIST---", test)
		for i := range test {
			p.CreateMessage(test[i], botUserId, l.Title, l.UrlLink, l.Identifier, l.CommentId, l.BotName, finalMessageBody, fromUser, l.MessageFrom, db, l.FileIds)
		}
	} else {
		newarray = remove(newarray, "@"+l.MessageFrom)
		fmt.Println(newarray)
		for i := range newarray {
			p.CreateMessage(newarray[i], botUserId, l.Title, l.UrlLink, l.Identifier, l.CommentId, l.BotName, finalMessageBody, fromUser, l.MessageFrom, db, l.FileIds)
		}
	}

}

func (p *Plugin) handleDialog(w http.ResponseWriter, req *http.Request) {

	request := model.SubmitDialogRequestFromJson(req.Body)

	user, uErr := p.API.GetUser(request.UserId)
	if uErr != nil {
		p.API.LogError(uErr.Error())
		return
	}

	T, _ := p.translation(user)
	location := p.location(user)

	message := request.Submission["message"]
	target := request.Submission["target"]
	ttime := request.Submission["time"]

	if target == nil {
		target = T("me")
	}
	if target != T("me") &&
		!strings.HasPrefix(target.(string), "@") &&
		!strings.HasPrefix(target.(string), "~") {
		target = "@" + target.(string)
	}

	var when string
	if ttime.(string) == "unit.test" {
		when = "in 20 minutes"
	} else {
		when = T("in") + " " + T("button.snooze."+ttime.(string))
		switch ttime.(string) {
		case "tomorrow":
			when = T("tomorrow")
		case "nextweek":
			when = T("monday")
		}
	}

	r := &ReminderRequest{
		TeamId:   request.TeamId,
		Username: user.Username,
		Payload:  message.(string),
		Reminder: Reminder{
			Id:        model.NewId(),
			TeamId:    request.TeamId,
			Username:  user.Username,
			Message:   message.(string),
			Completed: p.emptyTime,
			Target:    target.(string),
			When:      when,
		},
	}

	if cErr := p.CreateOccurrences(r); cErr != nil {
		p.API.LogError(cErr.Error())
		return
	}

	if rErr := p.UpsertReminder(r); rErr != nil {
		p.API.LogError(rErr.Error())
		return
	}

	if r.Reminder.Target == T("me") {
		r.Reminder.Target = T("you")
	}

	useTo := strings.HasPrefix(r.Reminder.Message, T("to"))
	var useToString string
	if useTo {
		useToString = " " + T("to")
	} else {
		useToString = ""
	}

	t := ""
	if len(r.Reminder.Occurrences) > 0 {
		t = r.Reminder.Occurrences[0].Occurrence.In(location).Format(time.RFC3339)
	}
	var responseParameters = map[string]interface{}{
		"Target":  r.Reminder.Target,
		"UseTo":   useToString,
		"Message": r.Reminder.Message,
		"When": p.formatWhen(
			r.Username,
			r.Reminder.When,
			t,
			false,
		),
	}

	reminder := &model.Post{
		ChannelId: request.ChannelId,
		UserId:    p.remindUserId,
		Props: model.StringInterface{
			"attachments": []*model.SlackAttachment{
				{
					Text: T("schedule.response", responseParameters),
					Actions: []*model.PostAction{
						{
							Integration: &model.PostActionIntegration{
								Context: model.StringInterface{
									"reminder_id":   r.Reminder.Id,
									"occurrence_id": r.Reminder.Occurrences[0].Id,
									"action":        "delete/ephemeral",
								},
								URL: fmt.Sprintf("/plugins/%s/delete/ephemeral", manifest.ID),
							},
							Type: model.POST_ACTION_TYPE_BUTTON,
							Name: T("button.delete"),
						},
						{
							Integration: &model.PostActionIntegration{
								Context: model.StringInterface{
									"reminder_id":   r.Reminder.Id,
									"occurrence_id": r.Reminder.Occurrences[0].Id,
									"action":        "view/ephemeral",
								},
								URL: fmt.Sprintf("/plugins/%s/view/ephemeral", manifest.ID),
							},
							Type: model.POST_ACTION_TYPE_BUTTON,
							Name: T("button.view.reminders"),
						},
					},
				},
			},
		},
	}
	p.API.SendEphemeralPost(user.Id, reminder)

}

func (p *Plugin) handleViewEphemeral(w http.ResponseWriter, r *http.Request) {

	request := model.PostActionIntegrationRequestFromJson(r.Body)

	user, uErr := p.API.GetUser(request.UserId)
	if uErr != nil {
		p.API.LogError(uErr.Error())
		writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
		return
	}
	p.API.SendEphemeralPost(user.Id, p.ListReminders(user, request.ChannelId))

	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})

}

func (p *Plugin) handleComplete(w http.ResponseWriter, r *http.Request) {

	request := model.PostActionIntegrationRequestFromJson(r.Body)

	reminder := p.GetReminder(request.Context["orig_user_id"].(string), request.Context["reminder_id"].(string))
	user, uErr := p.API.GetUser(request.UserId)
	if uErr != nil {
		p.API.LogError(uErr.Error())
		writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
		return
	}
	T, _ := p.translation(user)

	for _, occurrence := range reminder.Occurrences {
		p.ClearScheduledOccurrence(reminder, occurrence)
	}

	reminder.Completed = time.Now().UTC()
	p.UpdateReminder(request.Context["orig_user_id"].(string), reminder)

	if post, pErr := p.API.GetPost(request.PostId); pErr != nil {
		p.API.LogError("unable to get post " + pErr.Error())
		writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
	} else {

		user, uError := p.API.GetUser(request.UserId)
		if uError != nil {
			p.API.LogError(uError.Error())
			return
		}
		finalTarget := reminder.Target
		if finalTarget == T("me") {
			finalTarget = T("you")
		} else {
			finalTarget = "@" + user.Username
		}

		messageParameters := map[string]interface{}{
			"FinalTarget": finalTarget,
			"Message":     reminder.Message,
		}

		var updateParameters = map[string]interface{}{
			"Message": reminder.Message,
		}

		post.Message = "~~" + T("reminder.message", messageParameters) + "~~\n" + T("action.complete", updateParameters)
		post.Props = model.StringInterface{}
		p.API.UpdatePost(post)

		if reminder.Username != user.Username {
			if originalUser, uErr := p.API.GetUserByUsername(reminder.Username); uErr != nil {
				p.API.LogError(uErr.Error())
				writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
				return
			} else {
				if channel, cErr := p.API.GetDirectChannel(p.remindUserId, originalUser.Id); cErr != nil {
					p.API.LogError("failed to create channel " + cErr.Error())
					writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
				} else {
					var postbackUpdateParameters = map[string]interface{}{
						"User":    "@" + user.Username,
						"Message": reminder.Message,
					}
					if _, pErr := p.API.CreatePost(&model.Post{
						ChannelId: channel.Id,
						UserId:    p.remindUserId,
						Message:   T("action.complete.callback", postbackUpdateParameters),
					}); pErr != nil {
						p.API.LogError(pErr.Error())
						writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
					}
				}
			}
		}

		writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
	}

}

func (p *Plugin) handleDelete(w http.ResponseWriter, r *http.Request) {

	request := model.PostActionIntegrationRequestFromJson(r.Body)

	reminder := p.GetReminder(request.Context["orig_user_id"].(string), request.Context["reminder_id"].(string))
	user, uErr := p.API.GetUser(request.UserId)
	if uErr != nil {
		p.API.LogError(uErr.Error())
		writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
		return
	}
	T, _ := p.translation(user)

	for _, occurrence := range reminder.Occurrences {
		p.ClearScheduledOccurrence(reminder, occurrence)
	}

	message := reminder.Message
	p.DeleteReminder(request.Context["orig_user_id"].(string), reminder)

	if post, pErr := p.API.GetPost(request.PostId); pErr != nil {
		p.API.LogError(pErr.Error())
		writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
	} else {
		var deleteParameters = map[string]interface{}{
			"Message": message,
		}
		post.Message = T("action.delete", deleteParameters)
		post.Props = model.StringInterface{}
		p.API.UpdatePost(post)
		writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
	}

}

func (p *Plugin) handleDeleteEphemeral(w http.ResponseWriter, r *http.Request) {

	request := model.PostActionIntegrationRequestFromJson(r.Body)

	reminder := p.GetReminder(request.UserId, request.Context["reminder_id"].(string))
	user, uErr := p.API.GetUser(request.UserId)
	if uErr != nil {
		p.API.LogError(uErr.Error())
		writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
		return
	}
	T, _ := p.translation(user)

	for _, occurrence := range reminder.Occurrences {
		p.ClearScheduledOccurrence(reminder, occurrence)
	}

	message := reminder.Message
	p.DeleteReminder(request.UserId, reminder)

	var deleteParameters = map[string]interface{}{
		"Message": message,
	}
	post := &model.Post{
		Id:        request.PostId,
		UserId:    p.remindUserId,
		ChannelId: request.ChannelId,
		Message:   T("action.delete", deleteParameters),
	}
	p.API.UpdateEphemeralPost(request.UserId, post)
	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})

}

func (p *Plugin) handleSnooze(w http.ResponseWriter, r *http.Request) {

	request := model.PostActionIntegrationRequestFromJson(r.Body)

	reminder := p.GetReminder(request.Context["orig_user_id"].(string), request.Context["reminder_id"].(string))
	user, uErr := p.API.GetUser(request.UserId)
	if uErr != nil {
		p.API.LogError(uErr.Error())
		writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
		return
	}
	T, _ := p.translation(user)

	for _, occurrence := range reminder.Occurrences {
		if occurrence.Id == request.Context["occurrence_id"].(string) {
			p.ClearScheduledOccurrence(reminder, occurrence)
		}
	}

	if post, pErr := p.API.GetPost(request.PostId); pErr != nil {
		p.API.LogError("unable to get post " + pErr.Error())
		writePostActionIntegrationResponseError(w, &model.PostActionIntegrationResponse{})
	} else {
		var snoozeParameters = map[string]interface{}{
			"Message": reminder.Message,
		}

		switch request.Context["selected_option"].(string) {
		case "20min":
			for i, occurrence := range reminder.Occurrences {
				if occurrence.Id == request.Context["occurrence_id"].(string) {
					occurrence.Snoozed = time.Now().UTC().Round(time.Second).Add(time.Minute * time.Duration(20))
					reminder.Occurrences[i] = occurrence
					p.UpdateReminder(request.Context["orig_user_id"].(string), reminder)
					p.upsertSnoozedOccurrence(&occurrence)
					post.Message = T("action.snooze.20min", snoozeParameters)
					break
				}
			}
		case "1hr":
			for i, occurrence := range reminder.Occurrences {
				if occurrence.Id == request.Context["occurrence_id"].(string) {
					occurrence.Snoozed = time.Now().UTC().Round(time.Second).Add(time.Hour * time.Duration(1))
					reminder.Occurrences[i] = occurrence
					p.UpdateReminder(request.Context["orig_user_id"].(string), reminder)
					p.upsertSnoozedOccurrence(&occurrence)
					post.Message = T("action.snooze.1hr", snoozeParameters)
					break
				}
			}
		case "3hrs":
			for i, occurrence := range reminder.Occurrences {
				if occurrence.Id == request.Context["occurrence_id"].(string) {
					occurrence.Snoozed = time.Now().UTC().Round(time.Second).Add(time.Hour * time.Duration(3))
					reminder.Occurrences[i] = occurrence
					p.UpdateReminder(request.Context["orig_user_id"].(string), reminder)
					p.upsertSnoozedOccurrence(&occurrence)
					post.Message = T("action.snooze.3hr", snoozeParameters)
					break
				}
			}
		case "tomorrow":
			for i, occurrence := range reminder.Occurrences {
				if occurrence.Id == request.Context["occurrence_id"].(string) {

					if user, uErr := p.API.GetUser(request.UserId); uErr != nil {
						p.API.LogError(uErr.Error())
						return
					} else {
						location := p.location(user)
						tt := time.Now().In(location).Add(time.Hour * time.Duration(24))
						occurrence.Snoozed = time.Date(tt.Year(), tt.Month(), tt.Day(), 9, 0, 0, 0, location).UTC()
						reminder.Occurrences[i] = occurrence
						p.UpdateReminder(request.Context["orig_user_id"].(string), reminder)
						p.upsertSnoozedOccurrence(&occurrence)
						post.Message = T("action.snooze.tomorrow", snoozeParameters)
						break
					}
				}
			}
		case "nextweek":
			for i, occurrence := range reminder.Occurrences {
				if occurrence.Id == request.Context["occurrence_id"].(string) {

					if user, uErr := p.API.GetUser(request.UserId); uErr != nil {
						p.API.LogError(uErr.Error())
						return
					} else {
						location := p.location(user)

						todayWeekDayNum := int(time.Now().In(location).Weekday())
						weekDayNum := 1
						day := 0

						if weekDayNum < todayWeekDayNum {
							day = 7 - (todayWeekDayNum - weekDayNum)
						} else if weekDayNum >= todayWeekDayNum {
							day = 7 + (weekDayNum - todayWeekDayNum)
						}

						tt := time.Now().In(location).Add(time.Hour * time.Duration(24))
						occurrence.Snoozed = time.Date(tt.Year(), tt.Month(), tt.Day(), 9, 0, 0, 0, location).AddDate(0, 0, day).UTC()
						reminder.Occurrences[i] = occurrence
						p.UpdateReminder(request.Context["orig_user_id"].(string), reminder)
						p.upsertSnoozedOccurrence(&occurrence)
						post.Message = T("action.snooze.nextweek", snoozeParameters)
						break
					}
				}
			}
		}

		post.Props = model.StringInterface{}
		p.API.UpdatePost(post)
		writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
	}
}

func (p *Plugin) handleNextReminders(w http.ResponseWriter, r *http.Request) {
	request := model.PostActionIntegrationRequestFromJson(r.Body)
	p.UpdateListReminders(request.UserId, request.PostId, request.ChannelId, int(request.Context["offset"].(float64)))
	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
}

func (p *Plugin) handleCompleteList(w http.ResponseWriter, r *http.Request) {
	request := model.PostActionIntegrationRequestFromJson(r.Body)
	reminder := p.GetReminder(request.UserId, request.Context["reminder_id"].(string))

	for _, occurrence := range reminder.Occurrences {
		p.ClearScheduledOccurrence(reminder, occurrence)
	}

	reminder.Completed = time.Now().UTC()
	p.UpdateReminder(request.UserId, reminder)
	p.UpdateListReminders(request.UserId, request.PostId, request.ChannelId, int(request.Context["offset"].(float64)))
	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
}

func (p *Plugin) handleViewCompleteList(w http.ResponseWriter, r *http.Request) {
	request := model.PostActionIntegrationRequestFromJson(r.Body)
	p.ListCompletedReminders(request.UserId, request.PostId, request.ChannelId)
	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
}

func (p *Plugin) handleDeleteList(w http.ResponseWriter, r *http.Request) {
	request := model.PostActionIntegrationRequestFromJson(r.Body)
	reminder := p.GetReminder(request.UserId, request.Context["reminder_id"].(string))

	for _, occurrence := range reminder.Occurrences {
		p.ClearScheduledOccurrence(reminder, occurrence)
	}

	p.DeleteReminder(request.UserId, reminder)
	p.UpdateListReminders(request.UserId, request.PostId, request.ChannelId, int(request.Context["offset"].(float64)))
	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
}

func (p *Plugin) handleDeleteCompleteList(w http.ResponseWriter, r *http.Request) {
	request := model.PostActionIntegrationRequestFromJson(r.Body)
	p.DeleteCompletedReminders(request.UserId)
	p.UpdateListReminders(request.UserId, request.PostId, request.ChannelId, int(request.Context["offset"].(float64)))
	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
}

func (p *Plugin) handleSnoozeList(w http.ResponseWriter, r *http.Request) {
	request := model.PostActionIntegrationRequestFromJson(r.Body)
	reminder := p.GetReminder(request.UserId, request.Context["reminder_id"].(string))

	for _, occurrence := range reminder.Occurrences {
		if occurrence.Id == request.Context["occurrence_id"].(string) {
			p.ClearScheduledOccurrence(reminder, occurrence)
		}
	}

	switch request.Context["selected_option"].(string) {
	case "20min":
		for i, occurrence := range reminder.Occurrences {
			if occurrence.Id == request.Context["occurrence_id"].(string) {
				occurrence.Snoozed = time.Now().UTC().Round(time.Second).Add(time.Minute * time.Duration(20))
				reminder.Occurrences[i] = occurrence
				p.UpdateReminder(request.UserId, reminder)
				p.upsertSnoozedOccurrence(&occurrence)
				break
			}
		}
	case "1hr":
		for i, occurrence := range reminder.Occurrences {
			if occurrence.Id == request.Context["occurrence_id"].(string) {
				occurrence.Snoozed = time.Now().UTC().Round(time.Second).Add(time.Hour * time.Duration(1))
				reminder.Occurrences[i] = occurrence
				p.UpdateReminder(request.UserId, reminder)
				p.upsertSnoozedOccurrence(&occurrence)
				break
			}
		}
	case "3hrs":
		for i, occurrence := range reminder.Occurrences {
			if occurrence.Id == request.Context["occurrence_id"].(string) {
				occurrence.Snoozed = time.Now().UTC().Round(time.Second).Add(time.Hour * time.Duration(3))
				reminder.Occurrences[i] = occurrence
				p.UpdateReminder(request.UserId, reminder)
				p.upsertSnoozedOccurrence(&occurrence)
				break
			}
		}
	case "tomorrow":
		for i, occurrence := range reminder.Occurrences {
			if occurrence.Id == request.Context["occurrence_id"].(string) {

				if user, uErr := p.API.GetUser(request.UserId); uErr != nil {
					p.API.LogError(uErr.Error())
					return
				} else {
					location := p.location(user)
					tt := time.Now().In(location).Add(time.Hour * time.Duration(24))
					occurrence.Snoozed = time.Date(tt.Year(), tt.Month(), tt.Day(), 9, 0, 0, 0, location).UTC()
					reminder.Occurrences[i] = occurrence
					p.UpdateReminder(request.UserId, reminder)
					p.upsertSnoozedOccurrence(&occurrence)
					break
				}
			}
		}
	case "nextweek":
		for i, occurrence := range reminder.Occurrences {
			if occurrence.Id == request.Context["occurrence_id"].(string) {

				if user, uErr := p.API.GetUser(request.UserId); uErr != nil {
					p.API.LogError(uErr.Error())
					return
				} else {
					location := p.location(user)

					todayWeekDayNum := int(time.Now().In(location).Weekday())
					weekDayNum := 1
					day := 0

					if weekDayNum < todayWeekDayNum {
						day = 7 - (todayWeekDayNum - weekDayNum)
					} else if weekDayNum >= todayWeekDayNum {
						day = 7 + (weekDayNum - todayWeekDayNum)
					}

					tt := time.Now().In(location).Add(time.Hour * time.Duration(24))
					occurrence.Snoozed = time.Date(tt.Year(), tt.Month(), tt.Day(), 9, 0, 0, 0, location).AddDate(0, 0, day).UTC()
					reminder.Occurrences[i] = occurrence
					p.UpdateReminder(request.UserId, reminder)
					p.upsertSnoozedOccurrence(&occurrence)
					break
				}
			}
		}
	}

	p.UpdateListReminders(request.UserId, request.PostId, request.ChannelId, int(request.Context["offset"].(float64)))
	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
}

func (p *Plugin) handleCloseList(w http.ResponseWriter, r *http.Request) {
	request := model.PostActionIntegrationRequestFromJson(r.Body)
	p.API.DeleteEphemeralPost(request.UserId, request.PostId)
	writePostActionIntegrationResponseOk(w, &model.PostActionIntegrationResponse{})
}

func writePostActionIntegrationResponseOk(w http.ResponseWriter, response *model.PostActionIntegrationResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response.ToJson())
}

func writePostActionIntegrationResponseError(w http.ResponseWriter, response *model.PostActionIntegrationResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(response.ToJson())
}

func setupConnection(dataSourceName string, settings model.SqlSettings) (*sql.DB, error) {
	driverName := *settings.DriverName
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open SQL connection")
	}

	db.SetMaxOpenConns(500)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(time.Minute * 4)
	// db.SetConnMaxLifetime(time.Duration(*settings.ConnMaxLifetimeMilliseconds) * time.Millisecond)

	return db, nil
}

func (p *Plugin) HandleMsgOnUpdate(newPost, oldPost *model.Post) {
	fmt.Println("NEW POST---", newPost)
	fmt.Println("OLD POST---", oldPost)
	config := p.API.GetUnsanitizedConfig()

	db, errn := setupConnection(*config.SqlSettings.DataSource, config.SqlSettings)
	if errn != nil {
		p.API.LogError("failed to connect to master db" + errn.Error())
		return
	}
	if newPost != nil {
		if newPost.Props["from_bot"] == "true" {
			return
		}
		var PostId string
		var BotUserName string
		var CommentId string
		var FileId string
		errm := db.QueryRow("select PostId,BotUserName from PostFileHistory where ChannelId = ?", newPost.ChannelId).Scan(&PostId, &BotUserName)
		if errm == sql.ErrNoRows {
			fmt.Println("----Non Bot Channels----")
			return
		} else {
			botUser, bErr := p.API.GetUserByUsername(BotUserName)
			if bErr != nil {
				p.API.LogError("failed to find Bot user " + BotUserName)
				return
			}
			errk := db.QueryRow("select PostCommentHistory.CommentId, PostFileHistory.FileId from PostCommentHistory JOIN PostFileHistory On PostFileHistory.Id = PostCommentHistory.PostFileHistoryId where PostCommentHistory.PostId = ?", newPost.Id).Scan(&CommentId, &FileId)
			if errk == sql.ErrNoRows {
				interactivePost := model.Post{
					ChannelId:     newPost.ChannelId,
					PendingPostId: model.NewId() + ":" + fmt.Sprint(model.GetMillis()),
					UserId:        botUser.Id,
					Message:       ">#### **Unknown Post can't be updated(This Message is not sent)**",
					Props:         model.StringInterface{},
				}
				if _, pErr := p.API.CreatePost(&interactivePost); pErr != nil {
					p.API.LogError(fmt.Sprintf("%v", pErr))
				}
			} else {
				fmt.Println("Editing an existing comment/post---")
				if strings.Contains(newPost.Message, "@") {
					re := regexp.MustCompile("@(\\w+)?") // RegEx to match @
					fmt.Println(re)
					test1 := re.FindAllString(newPost.Message, -1) //Finding all occurances of @ in the given string
					for i := range test1 {
						targetPostUser := strings.Trim(test1[i], "@")
						postUser, pErr := p.API.GetUserByUsername(targetPostUser)
						if pErr != nil {
							p.API.LogError("failed to find target user " + targetPostUser)
							return
						}
						postUserId := postUser.Id
						fmt.Println("POST USERID---", postUserId)
						channel, cErr := p.API.GetDirectChannel(botUser.Id, postUserId)
						if cErr != nil {
							p.API.LogError("failed to create channel " + cErr.Error())
							return
						}
						fmt.Println("Channel Id---", channel.Id)
						var Id string
						err := db.QueryRow("select Id from Posts where ChannelId = ? AND UserId = ? AND Message LIKE ?", channel.Id, botUser.Id, "%"+oldPost.Message+"%").Scan(&Id)
						if err == sql.ErrNoRows {
							fmt.Println("No Post Found---")
							return
						} else {
							fmt.Println("----POST---", Id)
							oldComment, err := p.API.GetPost(Id)
							if err != nil {
								fmt.Println("No Post Found with given ID---")
							}
							patch := &model.PostPatch{}
							oldMsgBody := strings.Split(oldComment.Message, "/>) :")
							oldMsgBodyWithoutOldMsg := oldMsgBody[len(oldMsgBody)-2]
							patch.Message = model.NewString(oldMsgBodyWithoutOldMsg + "/>) : " + newPost.Message)

							oldComment.Patch(patch)
							if _, appErr := p.API.UpdatePost(oldComment); appErr != nil {
								p.API.LogError("failed to update post" + appErr.Error())
							}
						}
					}
				} else {
					// Handle edit observers list when there is no @ in the message body
					var CommentsId string
					errb := db.QueryRow("select CommentId from PostCommentHistory where PostId = ?", newPost.Id).Scan(&CommentsId)
					if errb == sql.ErrNoRows {
						fmt.Println("No Comment ID found for the given post---")
					}
					var NewPostId string
					rows, errc := db.Query("select PostId from PostCommentHistory where CommentId = ? AND PostId != ?", CommentsId, newPost.Id)
					if errc != nil {
						fmt.Println("No matching post---")
						log.Fatal(errc)
					}
					defer rows.Close()
					for rows.Next() {
						err := rows.Scan(&NewPostId)
						if err != nil {
							log.Fatal(err)
						}
						fmt.Println("NewPostId---", NewPostId)
						oldCommentList, err := p.API.GetPost(NewPostId)
						if err != nil {
							fmt.Println("No Post Found with given ID---")
						}
						patchList := &model.PostPatch{}
						fmt.Println("oldCommentList---", oldCommentList)
						fmt.Println(" oldMsgBodyList zmessage------", oldCommentList.Message)
						fmt.Println(" oldMsgBodyList leng------", len(oldCommentList.Message))
						oldMsgBodyList := strings.Split(oldCommentList.Message, "/>) :")
						fmt.Println(" oldMsgBodyList------", oldMsgBodyList)
						oldMsgBodyWithoutOldMsgList := oldMsgBodyList[len(oldMsgBodyList)-2]
						fmt.Println(" oldMsgBodyWithoutOldMsgList------", oldMsgBodyWithoutOldMsgList)
						patchList.Message = model.NewString(oldMsgBodyWithoutOldMsgList + "/>) : " + newPost.Message)

						oldCommentList.Patch(patchList)
						if _, appErr := p.API.UpdatePost(oldCommentList); appErr != nil {
							p.API.LogError("failed to update post" + appErr.Error())
						}
					}
					err := rows.Err()
					if err != nil {
						log.Fatal(err)
					}
				}

				var jsonStr = url.Values{
					"FileId":    {FileId},
					"text":      {newPost.Message},
					"senderId":  {newPost.UserId},
					"CommentId": {CommentId},
				}
				url := "callback/chat/postfilecomment/" + CommentId
				fmt.Println("Attachment URL---", url)
				req, errs := http.PostForm(url, jsonStr)
				if errs != nil {
					p.API.LogError("failed to connect to master db" + errs.Error())
					return
				}
				fmt.Println(req)
			}
		}
	}
}

func (p *Plugin) putRequest(url string, data io.Reader) {
	client := &http.Client{}
	req, err := http.NewRequest(http.MethodPut, url, data)
	req.Header.Add("Content-Type", "application/json")
	if err != nil {
		// handle error
		p.API.LogError("failed to Update" + err.Error())
	}
	_, err = client.Do(req)
	if err != nil {
		// handle error
		p.API.LogError("failed to update" + err.Error())
	}
}

func GetFileContentType(out *os.File) (string, error) {

	// Only the first 512 bytes are used to sniff the content type.
	buffer := make([]byte, 512)

	_, err := out.Read(buffer)
	if err != nil {
		return "", err
	}

	// Use the net/http package's handy DectectContentType function. Always returns a valid
	// content-type by returning "application/octet-stream" if no others seemed to match.
	contentType := http.DetectContentType(buffer)

	return contentType, nil
}

// Creates a new file upload http request with optional extra params
func newfileUploadRequest(uri string, params map[string]string, paramName, path string) (*http.Request, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(paramName, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(part, file)

	for key, val := range params {
		_ = writer.WriteField(key, val)
	}
	err = writer.Close()
	if err != nil {
		return nil, err
	}

	request, ers := http.NewRequest("POST", uri, body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request, ers
}

// Creates a new file upload http request with optional extra params
// func newfileUploadRequest(uri string, paramName, path string) (*http.Request, error) {
// 	file, err := os.Open(path)
// 	if err != nil {
// 		return nil, err
// 	}
// 	fileContents, err := ioutil.ReadAll(file)
// 	if err != nil {
// 		return nil, err
// 	}
// 	fi, err := file.Stat()
// 	if err != nil {
// 		return nil, err
// 	}
// 	file.Close()

// 	body := new(bytes.Buffer)
// 	writer := multipart.NewWriter(body)
// 	fmt.Println("---Uploaded File Name---", fi.Name())
// 	part, err := writer.CreateFormFile(paramName, fi.Name())
// 	if err != nil {
// 		return nil, err
// 	}
// 	part.Write(fileContents)

// 	err = writer.Close()
// 	if err != nil {
// 		return nil, err
// 	}

// 	request, ers := http.NewRequest("POST", uri, body)
// 	request.Header.Add("Content-Type", writer.FormDataContentType())
// 	return request, ers
// }

func (p *Plugin) HandleMsgFromDebuggingChannel(post *model.Post) {
	config := p.API.GetUnsanitizedConfig()

	db, err := setupConnection(*config.SqlSettings.DataSource, config.SqlSettings)
	if err != nil {
		p.API.LogError("failed to connect to master db" + err.Error())
		return
	}
	fmt.Println("POST-----", post)
	if post != nil {
		if post.Props["from_bot"] == "true" {
			return
		}
		var PostId string
		var BotUserName string
		err = db.QueryRow("select PostId,BotUserName from PostFileHistory where ChannelId = $1", post.ChannelId).Scan(&PostId, &BotUserName)
		p.API.LogError(fmt.Sprintf("%v", err))
		if err == sql.ErrNoRows {
			fmt.Println("Non Bot Channels")
			return
		} else {
			if post.RootId != "" {
				fmt.Println("Here bz RootID not null")
				var NonThreadPostChannelId string
				err = db.QueryRow("select ChannelId from PostFileHistory where PostId = $1", post.RootId).Scan(&NonThreadPostChannelId)
				fmt.Println("NonThreadPostChannelId-------", NonThreadPostChannelId)
				if err == sql.ErrNoRows {
					fmt.Println("NO Rows NonThreadPostChannelId---------- ")
					post.RootId = ""
				}
			}
			if post.RootId == "" {
				botUser, bErr := p.API.GetUserByUsername(BotUserName)
				if bErr != nil {
					p.API.LogError("failed to find Bot user " + BotUserName)
					return
				}

				interactivePost := model.Post{
					ChannelId:     post.ChannelId,
					PendingPostId: model.NewId() + ":" + fmt.Sprint(model.GetMillis()),
					UserId:        botUser.Id,
					Message:       ">#### **Please Reply Back On the Thread. There is no file associated to the comment(This Message is not sent)**",
					Props:         model.StringInterface{},
				}
				if _, pErr := p.API.CreatePost(&interactivePost); pErr != nil {
					p.API.LogError(fmt.Sprintf("%v", pErr))
				}
			} else {
				var FileIds, UseCommentId, FileId, UsePostId, UseFileHistoryId string
				fmt.Println("hrereeeeeeeeee--")
				fmt.Println(post.Id)
				fileAttachment := db.QueryRow("select FileIds FROM Posts WHERE Id = $1", post.Id).Scan(&FileIds)
				fmt.Println("File Attachments---", fileAttachment)
				fmt.Println(FileIds)
				err = db.QueryRow("select FileId from PostFileHistory where PostId = $1", post.RootId).Scan(&FileId)
				fmt.Println("FileId---", FileId)
				if err == sql.ErrNoRows {
					fmt.Println("NO Rows Found For the Query")
				}
				err = db.QueryRow("select PostId, Id from PostFileHistory where FileId = $1 AND UserId = $2 ORDER BY Id DESC LIMIT $3", FileId, post.UserId, 1).Scan(&UsePostId, &UseFileHistoryId)
				fmt.Println("UsePostId NN---", UsePostId)
				if err == sql.ErrNoRows {
					fmt.Println("NO UsePostId Found")
				}
				err = db.QueryRow("select CommentId from PostCommentHistory where PostId = $1", UsePostId).Scan(&UseCommentId)
				fmt.Println("UseCommentId---", UseCommentId)
				if err == sql.ErrNoRows {
					fmt.Println("NO Rows--")
				}
				if post.Message == "" {
					if FileIds != `[]` {
						post.Message = "Please find the attachment below"
					}
				}
				detailedData := url.Values{
					"FileId":   {FileId},
					"text":     {post.Message},
					"senderId": {post.UserId},
					"postId":   {post.Id},
					"parent":   {UseCommentId},
				}
				url := eoxAPIUrl + "callback/chat/postfilecomment"
				fmt.Println("Detailed Data--")
				fmt.Println(detailedData)
				req, errs := http.PostForm(url, detailedData)
				req.Header.Add("Content-Type", "application/json")
				if errs != nil {
					p.API.LogError("Post error" + errs.Error())
				}
				response, _ := ioutil.ReadAll(req.Body)
				p.API.LogError("RESPONSE---")
				p.API.LogError(string(response))
				fmt.Println("im here after post")
				jsonByteArray := []byte(`[` + string(response) + `]`)
				var things []Check
				errss := json.Unmarshal(jsonByteArray, &things)
				if errss != nil {
					p.API.LogError("Post error" + errss.Error())
				}
				fmt.Println("---Comment UUID---", things[0].Data.Uuid)
				if err != nil {
					p.API.LogError("Post error" + err.Error())
				}
				var AttachmentPath, AttachmentPreview, AttachmentThumbnail, AttachmentName string
				fmt.Println(FileIds != `[]`)
				if FileIds != `[]` {
					fmt.Println(FileIds)
					var FileAttachmentJson string
					FileIds = RemoveQuotes(FileIds)
					replacer := strings.NewReplacer(",", " ", "[", "", "]", "")
					FileIds = replacer.Replace(FileIds)
					FileIdList := strings.Fields(FileIds)
					fmt.Println(FileIdList)
					var commentsFileAttachment []CommentAttachments
					strPointerValue := *config.FileSettings.Directory
					// baseUrl := *config.ServiceSettings.UserProfileUrl
					// fmt.Println("BaseUrl-----", baseUrl)
					for i, fileAttachmentId := range FileIdList {
						fmt.Println(i, " => ", fileAttachmentId)
						errs := db.QueryRow("select Name, Path, ThumbnailPath, PreviewPath from FileInfo where Id = $1", fileAttachmentId).Scan(&AttachmentName, &AttachmentPath, &AttachmentThumbnail, &AttachmentPreview)
						if errs == sql.ErrNoRows {
							fmt.Println("NO FFF")
						}
						fmt.Println("FILE PATH fileAttachmentId----", fileAttachmentId)
						commentsFileAttachment = append(commentsFileAttachment, CommentAttachments{Name: AttachmentName, Path: filepath.Join(strPointerValue, AttachmentPath), Thumbnail: filepath.Join(strPointerValue, AttachmentThumbnail), Preview: filepath.Join(strPointerValue, AttachmentPreview)})
						result, err2 := json.Marshal(commentsFileAttachment)
						if err2 != nil {
							log.Println(err2)
						}
						FileAttachmentJson = `{"attachments" :` + string(result) + `}`
						fmt.Println(FileAttachmentJson)
						fmt.Println(string(result))

						filePath := path.Join(strPointerValue, AttachmentPath)
						urlToAttachment := eoxAPIUrl + "comment/" + things[0].Data.Uuid + "/saveCommentAttachment"
						fmt.Println(urlToAttachment)
						fmt.Println(filePath)
						extraParams := map[string]string{
							"title":       "My Document",
							"author":      "Matt Aimonetti",
							"description": "A document with all the Go programming language secrets",
						}
						request, err := newfileUploadRequest(urlToAttachment, extraParams, "file", filePath)
						if err != nil {
							log.Fatal(err)
						}
						client := &http.Client{}
						resp, err := client.Do(request)
						// resp, err := http.DefaultClient.Do(request)
						if err != nil {
							log.Fatal(err)
						} else {
							body := &bytes.Buffer{}
							_, err := body.ReadFrom(resp.Body)
							if err != nil {
								log.Fatal(err)
							}
							resp.Body.Close()
							fmt.Println("ATTACHMENT RESPON CODE----")
							fmt.Println(resp.StatusCode)
							fmt.Println(resp.Header)
							fmt.Println(body)
							// var bodyContent []byte
							// fmt.Println(resp.StatusCode)
							// fmt.Println(resp.Header)
							// resp.Body.Read(bodyContent)
							// resp.Body.Close()
							// fmt.Println(bodyContent)
						}
					}
				}
				fmt.Println("Inserting into postfilehistory-----")
				fmt.Println("post id====", post.Id)
				fmt.Println("fileid====", things[0].Data.FileId)
				fmt.Println("UserId====", post.UserId)
				fmt.Println("ChannelId====", post.ChannelId)
				fmt.Println("BotUserName====", BotUserName)
				res, err := db.Exec("INSERT INTO PostFileHistory (PostId, FileId, UserId, ChannelId, BotUserName) VALUES ($1, $2, $3, $4, $5)", post.Id, things[0].Data.FileId, post.UserId, post.ChannelId, BotUserName)
				if err != nil {
					fmt.Println("Unable to Insert-----")
					fmt.Println(err)
					panic(err)
				}
				prdID, err := res.LastInsertId()
				fmt.Println("---===", prdID)
				_, err = db.Exec("INSERT INTO PostCommentHistory (PostFileHistoryId, PostId, CommentId, ParentPostId) VALUES ($1, $2, $3, $4)", prdID, post.Id, things[0].Data.Uuid, post.RootId)
			}
		}
	}
}

func remove(s []string, r string) []string {
	for i, v := range s {
		if v == r {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

func (p *Plugin) MessageHasBeenPosted(c *plugin.Context, post *model.Post) {
	p.HandleMsgFromDebuggingChannel(post)
}

func (p *Plugin) MessageHasBeenUpdated(c *plugin.Context, newPost, oldPost *model.Post) {
	fmt.Println("MessageHasBeenUpdated-----")
	p.HandleMsgOnUpdate(newPost, oldPost)
}

func RemoveQuotes(s string) string {
	re := regexp.MustCompile(`"`)
	return re.ReplaceAllString(s, "")
}

func (p *Plugin) CreateMessage(newarray string, botUserId string, title string, urlLink string, identifier string, commentId string, botName string, finalMessageBody string, fromUser *model.User, messageFrom string, db *sql.DB, postIds string) {
	config := p.API.GetUnsanitizedConfig()
	var targetId, PostId string
	T, _ := p.translation(fromUser)
	target := strings.Trim(newarray, "@")
	targetUser, tErr := p.API.GetUserByUsername(target)
	if tErr != nil {
		p.API.LogError("failed to find target user " + target)
		return
	}
	targetId = targetUser.Id
	channel, cErr := p.API.GetDirectChannel(botUserId, targetId)
	if cErr != nil {
		p.API.LogError("failed to create channel " + cErr.Error())
		return
	}
	var appLink = "[" + title + "](" + urlLink + ")"
	errs := db.QueryRow("select PostId from PostFileHistory where FileId = ? and UserId = ?", identifier, targetId).Scan(&PostId)
	if errs == sql.ErrNoRows {
		fmt.Println("NO ROWS FOUND")
		interactivePost := model.Post{
			ChannelId:     channel.Id,
			PendingPostId: model.NewId() + ":" + fmt.Sprint(model.GetMillis()),
			UserId:        botUserId,
			Message:       "Message Thread For: " + appLink,
			Props:         model.StringInterface{},
		}
		posted, pErr := p.API.CreatePost(&interactivePost)
		if pErr != nil {
			p.API.LogError(fmt.Sprintf("%v", pErr))
		}
		fmt.Println("M hereOkk")
		fmt.Println(posted.Id)
		fmt.Println(identifier)
		p.API.LogInfo("Inserting into postfilehistory with query ", "INSERT INTO PostFileHistory (PostId, FileId, UserId, ChannelId, BotUserName) VALUES (?, ?, ?, ?, ?)", posted.Id, identifier, targetId, channel.Id, botName)
		res, err := db.Exec("INSERT INTO PostFileHistory (PostId, FileId, UserId, ChannelId, BotUserName) VALUES (?, ?, ?, ?, ?)", posted.Id, identifier, targetId, channel.Id, botName)
		if err != nil {
			p.API.LogError(fmt.Sprintf("%v", err))
		}
		prdID, err := res.LastInsertId()
		PostId = posted.Id
		fmt.Println("---===", PostId)
		_, err = db.Exec("INSERT INTO PostCommentHistory (PostFileHistoryId, PostId, CommentId, ParentPostId) VALUES (?, ?, ?, ?)", prdID, posted.Id, commentId, `NULL`)
		if err != nil {
			p.API.LogError(fmt.Sprintf("%v", err))
		}
	}
	fileList := make([]string, 0)
	fmt.Println("ATCHHDDD ffff---", postIds)
	fmt.Println(" checkin----", postIds != "")
	if postIds != "" {
		rows, errc := db.Query("select Name, Path from FileInfo where PostId = ?", postIds)
		if errc != nil {
			log.Fatal(errc)
		}
		defer rows.Close()
		for rows.Next() {
			var attachedFileName, attachedFilePath string
			err := rows.Scan(&attachedFileName, &attachedFilePath)
			if err != nil {
				log.Fatal(err)
			}
			strPointerValue := *config.FileSettings.Directory
			attachedFilePathInfo := filepath.Join(strPointerValue, attachedFilePath)
			fmt.Println("------attachedFilePathInfo---", attachedFilePathInfo)
			file, err := os.Open(attachedFilePathInfo)
			if err != nil {
				fmt.Println("------Unable to open file---")
				log.Fatal(err)
			}
			defer file.Close()

			data := &bytes.Buffer{}
			_, err = io.Copy(data, file)
			if err != nil {
				fmt.Println("------BUFFER COPY ERROR---")
				log.Fatal(err)
			}
			fileInfo, appErr := p.API.UploadFile(data.Bytes(), channel.Id, attachedFileName)
			fmt.Println("----------FILE INFO-----------", fileInfo)
			if appErr != nil {
				log.Fatal(appErr)
			}
			errsd := rows.Err()
			if errsd != nil {
				log.Fatal(errsd)
			}
			fileList = append(fileList, fileInfo.Id)
		}
		rows.Close()
	}
	if len(fileList) == 0 {
		//handle this case
		fmt.Println("----------NO attachments------")
	}
	fmt.Println(fileList)
	var messageParameters = map[string]interface{}{
		"FinalTarget": "@" + messageFrom,
		"Message":     finalMessageBody,
		"Title":       appLink,
	}
	interactivePostNew := model.Post{
		ChannelId:     channel.Id,
		PendingPostId: model.NewId() + ":" + fmt.Sprint(model.GetMillis()),
		UserId:        botUserId,
		Message:       T("reminder.message", messageParameters),
		Props:         model.StringInterface{},
		ParentId:      PostId,
		RootId:        PostId,
		FileIds:       fileList,
	}
	postsNew, pErr := p.API.CreatePost(&interactivePostNew)
	if pErr != nil {
		p.API.LogError(fmt.Sprintf("%v", pErr))
	}
	fmt.Println(postsNew)

	formattedquery := fmt.Sprintf("INSERT INTO PostFileHistory (PostId, FileId, UserId, ChannelId, BotUserName) VALUES ('%s', '%s', '%s', '%s', '%s')",
		postsNew.Id, identifier, targetId, channel.Id, botName)
	p.API.LogInfo(formattedquery)
	//res, err := db.Exec("INSERT INTO PostFileHistory (PostId, FileId, UserId, ChannelId, BotUserName) VALUES (?, ?, ?, ?, ?)", postsNew.Id, identifier, targetId, channel.Id, botName)
	res, err := db.Exec(formattedquery)
	if err != nil {
		p.API.LogError("failed to insert into PostFileHistory " + err.Error())
		return
		// panic(err)
	}
	prdID, err := res.LastInsertId()
	fmt.Println("---===")
	formattedquery = fmt.Sprintf("INSERT INTO PostCommentHistory (PostFileHistoryId, PostId, CommentId, ParentPostId) VALUES ('%d', '%s', '%s', '%s')", prdID, postsNew.Id, commentId, PostId)
	p.API.LogInfo(formattedquery)
	_, err = db.Exec(formattedquery)
	if err != nil {
		p.API.LogError("failed to insert into PostCommentHistory " + err.Error())
		return
		// panic(err)
	}
}
