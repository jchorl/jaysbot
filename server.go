package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/urlfetch"
	"net/http"
	"strconv"
	"time"
)

type Linescore struct {
	Runs struct {
		Home int `json:"home,string"`
		Away int `json:"away,string"`
	} `json:"r"`
}

type Alert struct {
	Text      string    `json:"text"`
	BriefText string    `json:"brief_text"`
	Updated   time.Time `json:"-"`
}

type Game struct {
	HomeTeamCity string    `json:"home_team_city"`
	AwayTeamCity string    `json:"away_team_city"`
	Alerts       Alert     `json:"alerts"`
	Linescore    Linescore `json:"linescore"`
}

type MLBResponse struct {
	Data struct {
		Games struct {
			Game []Game `json:"game"`
		} `json:"games"`
	} `json:"data"`
}

type HipChatNotification struct {
	Color         string `json:"color"`
	Message       string `json:"message"`
	Notify        bool   `json:"notify"`
	MessageFormat string `json:"message_format"`
}

type SlackNotification struct {
	Attachments []SlackAttachment `json:"attachments"`
}

type SlackAttachment struct {
	Fallback string `json:"fallback"`
	Color    string `json:"color"`
	Text     string `json:"text"`
}

func init() {
	http.HandleFunc("/poll_mlb", mlb_handler)
}

func send_to_hipchat(message string, color string, context context.Context) (*http.Response, error) {
	req := HipChatNotification{
		Color:         color,
		Message:       message,
		Notify:        false,
		MessageFormat: "text",
	}
	client := urlfetch.Client(context)
	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("https://api.hipchat.com/v2/room/%d/notification?auth_token=%s", ROOM, KEY)
	return client.Post(url, "application/json", bytes.NewBuffer(encoded))
}

func send_to_slack(message string, color string, context context.Context) (*http.Response, error) {
	req := SlackNotification{
		Attachments: []SlackAttachment{
			SlackAttachment{
				Color: color,
				Text:  message,
			},
		},
	}
	client := urlfetch.Client(context)
	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("https://hooks.slack.com/services/%s/%s/%s", TVAL, BVAL, KVAL)
	log.Infof(context, "POST %s to %s", encoded, url)
	return client.Post(url, "application/json", bytes.NewBuffer(encoded))
}

func mlb_handler(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	// prepare the request
	timezone, err := time.LoadLocation("America/New_York")
	if err != nil {
		log.Criticalf(c, "%s", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	year, month, day := time.Now().In(timezone).Date()
	dayString := strconv.Itoa(day)
	if len(dayString) == 1 {
		dayString = fmt.Sprint(0, dayString)
	}
	monthString := strconv.Itoa(int(month))
	if len(monthString) == 1 {
		monthString = fmt.Sprint(0, monthString)
	}

	// make the call
	client := urlfetch.Client(c)
	response, err := client.Get(fmt.Sprint("http://gd2.mlb.com/components/game/mlb/year_", year, "/month_", monthString, "/day_", dayString, "/master_scoreboard.json"))
	if err != nil {
		log.Criticalf(c, "%s", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// parse the response
	var parsed MLBResponse
	err = json.NewDecoder(response.Body).Decode(&parsed)
	if err != nil {
		log.Criticalf(c, "%s", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// find the jays game
	var jaysGame *Game
	areJaysHome := false
	games := parsed.Data.Games.Game
	for index, game := range games {
		if game.HomeTeamCity == "Toronto" {
			jaysGame = &games[index]
			areJaysHome = true
			break
		} else if game.AwayTeamCity == "Toronto" {
			jaysGame = &games[index]
			areJaysHome = false
			break
		}
	}

	if jaysGame != nil {
		// pull the last alert out of the db
		q := datastore.NewQuery("jays").Order("-Updated")
		var alerts []Alert
		keys, err := q.GetAll(c, &alerts)
		if err != nil {
			log.Criticalf(c, "%s", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		notify := false
		jaysGame.Alerts.Updated = time.Now()
		if len(alerts) == 0 {
			// put the new alert into the db
			_, err := datastore.Put(c, datastore.NewIncompleteKey(c, "jays", nil), &jaysGame.Alerts)
			if err != nil {
				log.Criticalf(c, "%s", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			notify = true
		} else {
			// take the latest entity
			latest := alerts[0]

			// check if it is different
			if latest.BriefText != jaysGame.Alerts.BriefText {
				_, err = datastore.Put(c, keys[0], &jaysGame.Alerts)
				if err != nil {
					log.Criticalf(c, "%s", err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				notify = true
			}
		}

		if notify {
			color := "#808080"
			if areJaysHome {
				if jaysGame.Linescore.Runs.Home > jaysGame.Linescore.Runs.Away {
					color = "#00cc00"
				} else if jaysGame.Linescore.Runs.Away > jaysGame.Linescore.Runs.Home {
					color = "#e50000"
				}
			} else {
				if jaysGame.Linescore.Runs.Home > jaysGame.Linescore.Runs.Away {
					color = "#e50000"
				} else if jaysGame.Linescore.Runs.Away > jaysGame.Linescore.Runs.Home {
					color = "#00cc00"
				}
			}
			_, err = send_to_slack(jaysGame.Alerts.BriefText, color, c)
			if err != nil {
				log.Criticalf(c, "%s", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
}
