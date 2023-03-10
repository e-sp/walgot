package tui

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"git.bacardi55.io/bacardi55/walgot/internal/api"
	"git.bacardi55.io/bacardi55/walgot/internal/config"

	"github.com/Strubbl/wallabago/v7"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ** Model related Struct ** //

// Terminal physical size:
type termSize struct {
	Width  int
	Height int
}

// TableView filter options
type walgotTableFilters struct {
	Archived bool
	Starred  bool
	Unread   bool
	Public   bool
	Search   string
}

// TableView Sort options
type walgotTableSorts struct {
	Field string
	Order string
}

// TableView options
type walgotTableOptions struct {
	Filters walgotTableFilters
	Sorts   walgotTableSorts
}

// Dialog Box:
type walgotDialog struct {
	Message   string
	TextInput textinput.Model
	ShowInput bool
	Action    string
}

// Walgot error message:
type wallabagoResponseErrorMsg struct {
	message        string
	wallabagoError error
}

// Model structure
type model struct {
	// Sub models related:
	Table         table.Model
	Viewport      viewport.Model
	Dialog        walgotDialog
	Spinner       spinner.Model
	UpdateMessage string
	// Tui Status related
	Ready       bool
	Reloading   bool
	CurrentView string
	Options     walgotTableOptions
	// Wallabag(o) related:
	Entries              []wallabago.Item
	SelectedID           int
	TotalEntriesOnServer int
	// Configs
	NbEntriesPerAPICall int
	TermSize            termSize
	DebugMode           bool
}

// NewModel returns default model for walgot.
func NewModel(config config.WalgotConfig) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.
		NewStyle().
		Foreground(lipgloss.Color("205"))

	return model{
		SelectedID:           0,
		Ready:                false,
		Reloading:            true,
		CurrentView:          "list",
		TotalEntriesOnServer: 0,
		Spinner:              s,
		NbEntriesPerAPICall:  config.NbEntriesPerAPICall,
		DebugMode:            config.DebugMode,
		Dialog: walgotDialog{
			Message:   "",
			ShowInput: false,
			TextInput: textinput.New(),
			Action:    "",
		},
		Options: walgotTableOptions{
			Filters: walgotTableFilters{
				Unread:  config.DefaultListViewUnread,
				Starred: config.DefaultListViewStarred,
				Public:  config.DefaultListViewPublic,
			},
			Sorts: walgotTableSorts{
				Field: "created",
				Order: "desc",
			},
		},
	}
}

// Response message for number of entities from Wallabago
type wallabagoResponseNbEntitiesMsg int

// Response message for all entities from Wallabago.
type wallabagoResponseEntitiesMsg []wallabago.Item

// Response message for entity update.
type wallabagoResponseEntityUpdateMsg struct {
	UpdatedEntry wallabago.Item
}

// After update message has been displayed enough time.
type wallabagoResponseClearMsg bool

// Add entry message.
type wallabagoResponseAddEntryMsg struct {
	Entry wallabago.Item
}

// Delete entry message.
type wallabagoResponseDeleteEntryMsg int

// Selected row in table list Message.
type walgotSelectRowMsg int

// Search for an entry message.
type walgotSearchEntryMsg string

// Callback for requesting the total number of entries via API.
func requestWallabagNbEntries() tea.Msg {
	// Get total number of articles:
	nbArticles, e := api.GetNbTotalEntries()

	if e != nil {
		return wallabagoResponseErrorMsg{
			message:        "Error:\n couldn't retrieve the total number of entries from wallabag API",
			wallabagoError: e,
		}
	}

	return wallabagoResponseNbEntitiesMsg(nbArticles)
}

// Callback for requesting entries via API.
func requestWallabagEntries(nbArticles, nbEntriesPerAPICall int, sortField, sortOrder string) tea.Cmd {
	cacheFilename := "/tmp/walgot-cache.dat"
	entries := []wallabago.Item{}
	// Load cache if present
	info, err := os.Stat(cacheFilename)
	if err == nil {
		if info.Mode()&1<<2 != 0 {
			// https://stackoverflow.com/questions/45429210/how-do-i-check-a-files-permissions-in-linux-using-go
			// other users have read permission
			return func() tea.Msg {
				return wallabagoResponseErrorMsg{
					message:        "Error:\n couldn't read cache file for security reasons",
					wallabagoError: err,
				}
			}
		}

		if content, err := os.ReadFile(cacheFilename); err == nil {
			err = gob.NewDecoder(bytes.NewBuffer(content)).Decode(&entries)
			if err != nil {
				return func() tea.Msg {
					return wallabagoResponseErrorMsg{
						message:        "Error:\n couldn't load the entries from cache",
						wallabagoError: err,
					}
				}
			}
		}
	}

	return func() tea.Msg {
		if len(entries) > 0 {
			return wallabagoResponseEntitiesMsg(entries)
		}
		limitArticleByAPICall := nbEntriesPerAPICall
		nbCalls := getRequiredNbAPICalls(nbArticles, limitArticleByAPICall)

		// TODO: Move this to async channel?
		// Might not be a good idea with the ELM architecture?
		// var entries []wallabago.Item
		for i := 1; i < nbCalls+1; i++ {
			r, err := api.GetEntries(limitArticleByAPICall, i, sortField, sortOrder)

			if err != nil {
				return wallabagoResponseErrorMsg{
					message:        "Error:\n couldn't retrieve the entries from wallabag API",
					wallabagoError: err,
				}
			}

			entries = append(entries, r.Embedded.Items...)
		}
		// TODO cache here, use args for deciding when to cache and when to
		// return from cache.  Might not be the best position anyways, since
		// sortField and sortOrder can be provided and may be used for more
		// specific queries, which would then possibly circumvent the cache,
		// provided the cache would work for queries without filters set.
		file, err := os.OpenFile(cacheFilename, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		defer file.Close()
		if err != nil {
			msg := "Error:\n couldn't open file to cache Wallabag entries: %s: %s"
			return wallabagoResponseErrorMsg{
				message:        fmt.Sprintf(msg, cacheFilename, err),
				wallabagoError: err,
			}
		}
		err = gob.NewEncoder(file).Encode(entries)
		if err != nil {
			return wallabagoResponseErrorMsg{
				message:        fmt.Sprintf("Error:\n couldn't decode cache file to Wallabag entries slice: %s", err),
				wallabagoError: err,
			}
		}

		return wallabagoResponseEntitiesMsg(entries)
	}
}

// Callback for updating an entry status via API.
func requestWallabagEntryUpdate(entryID, archive, starred, public int) tea.Cmd {
	return func() tea.Msg {
		// Send PATCH via API:
		r, err := api.UpdateEntry(entryID, archive, starred, public)
		if err != nil {
			return wallabagoResponseErrorMsg{
				message:        "Error:\n Couldn't update the selected entry",
				wallabagoError: err,
			}
		}

		var item wallabago.Item
		err = json.Unmarshal(r, &item)
		if err != nil {
			return wallabagoResponseErrorMsg{
				message:        "Error:\n Response from wallabago is not valid",
				wallabagoError: err,
			}
		}

		return wallabagoResponseEntityUpdateMsg{item}
	}
}

// Callback for adding an entry via API.
func requestWallabagAddEntry(url string) tea.Cmd {
	return func() tea.Msg {
		if !isValidURL(url) {
			return wallabagoResponseErrorMsg{
				message:        "Error:\n Invalid URL",
				wallabagoError: errors.New("invalid URL given"),
			}
		}
		r, err := api.AddEntry(url)
		if err != nil {
			return wallabagoResponseErrorMsg{
				message:        "Error:\n Couldn't add the entry",
				wallabagoError: err,
			}
		}

		return wallabagoResponseAddEntryMsg{
			Entry: r,
		}
	}
}

// Callback for deleting an entry via API.
func requestWallabagEntryDelete(id int) tea.Cmd {
	return func() tea.Msg {
		err := api.DeleteEntry(id)
		if err != nil {
			return wallabagoResponseErrorMsg{
				message:        "Error:\n Couldn't add the entry",
				wallabagoError: err,
			}
		}

		return wallabagoResponseDeleteEntryMsg(id)
	}
}

// Callback for selecting entry in list:
func selectEntryCommand(selectedRowID int) tea.Cmd {
	return func() tea.Msg {
		return walgotSelectRowMsg(selectedRowID)
	}
}

// ** Model related methods ** //
// Init method.
func (m model) Init() tea.Cmd {
	//wallabago.ReadConfig(m.WallabagConfig )

	return tea.Batch(
		requestWallabagNbEntries,
		m.Spinner.Tick,
	)
}

// Update method.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.DebugMode {
		log.Println(fmt.Sprintf("Update message received, type: %T", msg), m.CurrentView)
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		// C-c to kill the app.
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		} else if msg.String() == "?" && !m.Reloading {
			m.CurrentView = "help"
			return m, nil
		}
	}

	// Priority: Error > updates > entrySelection:
	if v, ok := msg.(wallabagoResponseErrorMsg); ok {
		m.Reloading = false
		if m.DebugMode {
			log.Println("Wallabago error:")
			log.Println(v.wallabagoError)
		}
		m.Dialog.Message = v.message
	} else if v, ok := msg.(wallabagoResponseEntityUpdateMsg); ok {
		// If received an entry update response message,
		// the model needs to be updated with refreshed entry:
		updatedEntryInModel(&m, v.UpdatedEntry)
		// To remove the update message after 3 seconds:
		return m, tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
			return wallabagoResponseClearMsg(true)
		})
	} else if v, ok := msg.(wallabagoResponseClearMsg); ok && bool(v) {
		// Clear update message
		m.UpdateMessage = ""
	} else if v, ok := msg.(walgotSelectRowMsg); ok {
		// This needs to happen before sending to the sub update function.
		m.SelectedID = int(v)
	}

	// Priority order: dialog > help > detail > list.
	if m.Dialog.Message != "" {
		return updateDialogView(msg, &m)
	} else if m.CurrentView == "help" {
		return updateHelpView(msg, m)
	}

	// Now send to the right sub-update function:
	if m.SelectedID > 0 {
		return updateEntryView(msg, &m)
	}
	return updateListView(msg, m)
}

// View method.
func (m model) View() string {
	return fmt.Sprintf("%s\n%s\n%s", m.headerView(), m.mainView(), m.footerView())
}
