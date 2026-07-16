package ui

import (
	"fmt"
	"image/color"
	"log"
	"net/url"
	"os"
	"os/user"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"

	"task-timer-app/internal/assets"
	"task-timer-app/internal/task"
)

const (
	appID = "com.tasktimer.app"

	// pollEvery is how often the table picks up work the daemon wrote
	// behind the app's back.
	pollEvery = 5 * time.Second

	// tickEvery drives the running clock. The display shows milliseconds, but
	// refreshing at 1kHz burned a core animating digits nobody can read.
	tickEvery = 10 * time.Millisecond

	sidebarWidth float32 = 232
	headerHeight float32 = 72

	// The window's opening size. Nothing on any page may need more width than
	// this: Fyne will not let a window be smaller than its content's MinSize, so
	// a page that overruns turns into a window the user cannot shrink.
	windowWidth  float32 = 1280
	windowHeight float32 = 800

	// recentLimit caps what the Dashboard and Tasks pages load. Reports go to
	// the store directly for the range they need.
	recentLimit = 500

	prefWorkingDayHours = "working_day_hours"
	defaultWorkingHours = 8.0
)

// App is the whole desktop interface: the window, the timer state machine, and
// the five pages behind the sidebar.
type App struct {
	fyne   fyne.App
	window fyne.Window
	store  *task.Store
	user   string

	// Timer state. Every mutation happens on the UI goroutine, so a mutex would
	// be ceremony.
	running   bool
	startTime time.Time
	// stopTick ends the display-refresh goroutine. Closing the channel rather
	// than calling Ticker.Stop is what stops each start leaking a goroutine that
	// lives until the process exits.
	stopTick chan struct{}

	// Data shared by the pages, reloaded from the store on a timer.
	tasks   []task.Task
	remotes map[string]task.Remote

	// Shell.
	nav      []*navEntry
	active   int
	body     *fyne.Container
	titleTxt *canvas.Text
	subTxt   *canvas.Text

	// Footer.
	footUser      *canvas.Text
	footTotal     *canvas.Text
	footRemaining *canvas.Text
	footTasks     *canvas.Text
	footClock     *canvas.Text

	// Pages, built once and kept alive so a running timer survives navigation.
	dashboard *dashboardPage
	taskList  *tasksPage
	reports   *reportsPage
	settings  *settingsPage
	about     *aboutPage
}

// navEntry binds a sidebar row to the page it shows.
type navEntry struct {
	item     *navItem
	title    string
	subtitle string
	content  fyne.CanvasObject
	refresh  func()
}

// New builds the application around an open store.
func New(store *task.Store) *App {
	return newWithApp(app.NewWithID(appID), store)
}

// newWithApp is New with the Fyne app supplied by the caller. The desktop
// binary uses New; the render tests pass a headless test app so the whole
// widget tree can be laid out and rasterised without a window server.
func newWithApp(fyneApp fyne.App, store *task.Store) *App {
	a := &App{
		fyne:    fyneApp,
		store:   store,
		user:    currentUserName(),
		remotes: map[string]task.Remote{},
	}

	a.fyne.Settings().SetTheme(taskTimerTheme{})
	a.fyne.SetIcon(appIcon())

	a.window = a.fyne.NewWindow("Task Timer")
	a.window.SetIcon(appIcon())
	a.window.Resize(fyne.NewSize(windowWidth, windowHeight))
	a.window.SetMaster()

	a.build()
	a.window.SetOnClosed(func() { _ = store.Close() })

	return a
}

// Run shows the window and blocks until the app exits.
func (a *App) Run() {
	go a.poll()
	go a.tickFooterClock()

	a.reload()
	a.window.ShowAndRun()
}

// appIcon wraps the embedded PNG as a Fyne resource.
func appIcon() fyne.Resource {
	return fyne.NewStaticResource("tasktimer.png", assets.AppIconPNG)
}

// currentUserName returns the logged-in username or a safe fallback.
func currentUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// Shell
// ---------------------------------------------------------------------------

func (a *App) build() {
	a.dashboard = newDashboardPage(a)
	a.taskList = newTasksPage(a)
	a.reports = newReportsPage(a)
	a.settings = newSettingsPage(a)
	a.about = newAboutPage(a)

	a.nav = []*navEntry{
		{title: "Overview", subtitle: "Track time and manage tasks efficiently",
			content: a.dashboard.content, refresh: a.dashboard.refresh},
		{title: "Tasks", subtitle: "Every work session this machine has recorded",
			content: a.taskList.content, refresh: a.taskList.refresh},
		{title: "Reports", subtitle: "Where your hours actually went",
			content: a.reports.content, refresh: a.reports.refresh},
		{title: "Settings", subtitle: "Working day, providers, and data location",
			content: a.settings.content, refresh: a.settings.refresh},
		{title: "About", subtitle: "Version and build information",
			content: a.about.content, refresh: a.about.refresh},
	}

	labels := []struct {
		text string
		icon func(color.Color) fyne.Resource
	}{
		{"Dashboard", iconDashboard},
		{"Tasks", iconList},
		{"Reports", iconReports},
		{"Settings", iconSettings},
		{"About", iconInfo},
	}
	for i, l := range labels {
		index := i
		a.nav[i].item = newNavItem(l.text, l.icon, func() { a.selectPage(index) })
	}

	a.body = container.NewStack(a.nav[0].content)
	a.nav[0].item.SetSelected(true)

	a.titleTxt = heading(a.nav[0].title)
	a.subTxt = muted(a.nav[0].subtitle)

	content := container.NewBorder(a.header(), nil, nil, nil,
		container.New(insetLayout{top: 0, right: gutter, bottom: gutter, left: gutter}, a.body))

	page := container.NewStack(canvas.NewRectangle(colPage), content)

	a.window.SetContent(container.NewBorder(
		nil, a.footer(), a.sidebar(), nil, page))
}

func (a *App) sidebar() fyne.CanvasObject {
	logoIcon := canvas.NewImageFromResource(iconClock(colText))
	logoIcon.FillMode = canvas.ImageFillContain
	logoIcon.SetMinSize(fyne.NewSize(26, 26))

	logoText := canvas.NewText("Task Timer", colText)
	logoText.TextSize = 17
	logoText.TextStyle.Bold = true

	logo := container.New(insetLayout{top: 22, right: 16, bottom: 20, left: 16},
		container.NewHBox(logoIcon, insetXY(logoText, 4, 0)))

	items := container.NewVBox()
	for _, entry := range a.nav {
		items.Add(entry.item)
	}

	// An invisible strut is what fixes the rail's width: Border sizes its left
	// child to the child's MinSize, and the nav labels alone are far narrower
	// than the design's rail.
	strut := canvas.NewRectangle(color.Transparent)
	strut.SetMinSize(fyne.NewSize(sidebarWidth, 0))

	column := container.NewVBox(
		logo,
		container.New(insetLayout{right: 12, left: 12}, items),
		strut,
	)

	return container.NewStack(
		canvas.NewRectangle(colSidebar),
		container.NewBorder(column, nil, nil, nil, nil),
	)
}

func (a *App) header() fyne.CanvasObject {
	title := container.NewVBox(
		layout.NewSpacer(),
		a.titleTxt,
		insetXY(a.subTxt, 0, 2),
		layout.NewSpacer(),
	)

	online := canvas.NewText("Online", colSuccess)
	online.TextSize = 11

	name := canvas.NewText(a.user, colText)
	name.TextSize = 13
	name.TextStyle.Bold = true

	identity := container.NewVBox(
		layout.NewSpacer(),
		name,
		container.NewHBox(dot(colSuccess, 7), online),
		layout.NewSpacer(),
	)

	chevron := canvas.NewImageFromResource(iconChevronDown(colTextMuted))
	chevron.FillMode = canvas.ImageFillContain
	chevron.SetMinSize(fyne.NewSize(14, 14))

	// The chevron is honest about what it does: the menu it opens is the same
	// one the tray carries.
	userChip := container.NewHBox(
		centreY(avatar(initials(a.user), 36)),
		insetXY(identity, 6, 0),
		centreY(chevron),
	)

	row := container.NewBorder(nil, nil, title, userChip)

	return container.New(insetLayout{top: 18, right: gutter, bottom: 14, left: gutter},
		sized(row, 0, headerHeight))
}

func (a *App) footer() fyne.CanvasObject {
	a.footUser = muted(fmt.Sprintf("Logged in as: %s", a.user))
	a.footTotal = muted("Total time today: 00:00:00")
	a.footRemaining = muted("Time remaining: 00:00:00")
	a.footTasks = muted("Tasks today: 0")

	// Seeded with the current time rather than left blank: the ticker below only
	// fires a second from now, and an empty clock in the corner reads as broken.
	a.footClock = muted(time.Now().Format(dateLayout + " " + clockLayout))

	sep := func() fyne.CanvasObject {
		s := canvas.NewRectangle(colBorder)
		s.SetMinSize(fyne.NewSize(1, 14))
		return centreY(s)
	}

	left := container.NewHBox(
		centreY(a.footUser), insetXY(sep(), 8, 0),
		centreY(a.footTotal), insetXY(sep(), 8, 0),
		centreY(a.footRemaining), insetXY(sep(), 8, 0),
		centreY(a.footTasks),
	)

	clockIcon := canvas.NewImageFromResource(iconClock(colTextMuted))
	clockIcon.FillMode = canvas.ImageFillContain
	clockIcon.SetMinSize(fyne.NewSize(14, 14))

	right := container.NewHBox(centreY(clockIcon), insetXY(centreY(a.footClock), 6, 0))

	bar := container.New(insetLayout{top: 10, right: gutter, bottom: 10, left: gutter},
		container.NewBorder(nil, nil, left, right))

	return container.NewStack(
		canvas.NewRectangle(colSidebar),
		container.NewBorder(hairline(), nil, nil, nil, bar),
	)
}

// selectPage swaps the body and re-titles the header.
func (a *App) selectPage(i int) {
	if i < 0 || i >= len(a.nav) {
		return
	}

	a.active = i
	for j, entry := range a.nav {
		entry.item.SetSelected(j == i)
	}

	entry := a.nav[i]

	a.titleTxt.Text = entry.title
	a.titleTxt.Refresh()
	a.subTxt.Text = entry.subtitle
	a.subTxt.Refresh()

	// Refreshed on arrival rather than on every poll: rebuilding five pages'
	// worth of rows every five seconds is work nobody is looking at.
	entry.refresh()

	a.body.Objects = []fyne.CanvasObject{entry.content}
	a.body.Refresh()
}

// ---------------------------------------------------------------------------
// Data
// ---------------------------------------------------------------------------

// reload pulls current state out of the store and refreshes whatever is on
// screen. It is a no-op mid-session: rebuilding the table under a running timer
// would fight with the row pinned to the top of it.
func (a *App) reload() {
	if a.running {
		return
	}

	tasks, err := a.store.Recent(recentLimit)
	if err != nil {
		a.reportError("Loading tasks", err)
		return
	}
	a.tasks = tasks

	a.reloadRemotes()
	a.nav[a.active].refresh()
	a.updateFooter()
	a.updateSystemTray()
}

// queuePush marks every unpushed session as ready to go upstream.
//
// It does not push anything itself, and deliberately so: the reconcile loop owns
// every conversation with the gateway, and a second pusher living in the UI
// process would be a second thing racing for the same rows. This writes a flag;
// the thread finds it on its next scan and does the work.
//
// Until this is pressed, nothing a user times leaves their machine. Push
// (see connect.go) is the button's actual entry point — it signs the machine in
// to the backend first when that has not happened yet, then calls this.
func (a *App) queuePush() {
	n, err := a.store.RequestPush()
	if err != nil {
		a.reportError("Requesting pushing", err)
		return
	}

	if n == 0 {
		dialog.ShowInformation("Nothing to push",
			"Every finished session linked to a tracked task has already been sent.",
			a.window)
		return
	}

	// The rows are queued, not sent. Saying "pushed" here would be a lie
	// the user finds out about later, when they go looking in their task tracker
	// for time that the daemon has not pushed yet.
	dialog.ShowInformation("Queued for pushing",
		fmt.Sprintf("%s queued. The daemon will send them to the gateway on its next pass.",
			plural(n, "session", "sessions")),
		a.window)

	a.reload()
}

// reloadRemotes rebuilds the map of tasks pulled from a provider. This is where
// a remote issue becomes something the user can put a timer against.
func (a *App) reloadRemotes() {
	remotes, err := a.store.OpenRemotes()
	if err != nil {
		a.reportError("Loading tracker tasks", err)
		return
	}

	a.remotes = make(map[string]task.Remote, len(remotes))
	for _, r := range remotes {
		a.remotes[r.DisplayName()] = r
	}
}

// taskOptions is the task picker's contents: every open local task name, plus
// every open task pulled from a provider.
func (a *App) taskOptions() []string {
	names, err := a.store.OpenNames()
	if err != nil {
		a.reportError("Loading task names", err)
		return nil
	}

	seen := map[string]bool{}
	options := make([]string, 0, len(names)+len(a.remotes))

	for name := range a.remotes {
		if !seen[name] {
			seen[name] = true
			options = append(options, name)
		}
	}
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			options = append(options, name)
		}
	}

	sort.Strings(options)
	return options
}

// poll refreshes in the background so work written by the daemon shows up
// without the user having to hit Refresh.
func (a *App) poll() {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()

	for range ticker.C {
		if !a.running {
			a.reload()
		}
	}
}

// ---------------------------------------------------------------------------
// Timer
// ---------------------------------------------------------------------------

// workingDay is the target the footer counts down from, in the Settings page's
// units.
func (a *App) workingDay() time.Duration {
	hours := a.fyne.Preferences().FloatWithFallback(prefWorkingDayHours, defaultWorkingHours)
	if hours <= 0 {
		hours = defaultWorkingHours
	}
	return time.Duration(hours * float64(time.Hour))
}

// elapsed is how long the running session has been going, or zero.
func (a *App) elapsed() time.Duration {
	if !a.running {
		return 0
	}
	return time.Since(a.startTime)
}

// Start begins timing the named task.
func (a *App) Start(name string) {
	if a.running || name == "" {
		return
	}

	a.running = true
	a.startTime = time.Now()
	a.stopTick = make(chan struct{})

	a.dashboard.timerStarted()
	a.updateFooter()
	a.updateSystemTray()

	go a.tick(a.stopTick, a.startTime)
}

// Stop ends the running session and records it.
func (a *App) Stop() {
	name := a.dashboard.taskName()
	if !a.commit(name) {
		return
	}
	a.reload()
}

// Lap records the time so far and immediately restarts the clock on the same
// task, without the user having to stop and start again. It is how you bank a
// chunk of work at a natural boundary — a commit, a meeting ending — while
// carrying on with the same thing.
func (a *App) Lap() {
	if !a.running {
		return
	}

	name := a.dashboard.taskName()
	if !a.commit(name) {
		return
	}

	// Restart before reloading: reload() bails out while the timer runs, and
	// the point of a lap is that it never stops.
	a.running = true
	a.startTime = time.Now()
	a.stopTick = make(chan struct{})

	a.dashboard.timerStarted()
	go a.tick(a.stopTick, a.startTime)

	a.refreshAfterWrite()
}

// Reset abandons the running session without recording it. It asks first: the
// whole point of the app is that timed work does not evaporate.
func (a *App) Reset() {
	if !a.running {
		a.dashboard.timerReset()
		return
	}

	elapsed := a.elapsed()
	dialog.ShowConfirm("Discard this session?",
		fmt.Sprintf("%s on %q will not be recorded.", clock(elapsed), a.dashboard.taskName()),
		func(discard bool) {
			if !discard {
				return
			}
			a.halt()
			a.dashboard.timerStopped()
			a.dashboard.timerReset()
			a.reload()
		}, a.window)
}

// commit stops the clock and writes the session. It reports whether the write
// happened, so the callers that carry on afterwards — Lap — know not to.
func (a *App) commit(name string) bool {
	if !a.running {
		return false
	}

	end := time.Now()
	start := a.startTime
	a.halt()
	a.dashboard.timerStopped()

	if name == "" {
		dialog.ShowInformation("Nothing to record", "The session has no task name.", a.window)
		return false
	}

	t := task.Task{
		Name:       name,
		Start:      start,
		End:        end,
		Duration:   end.Sub(start),
		AssignedBy: a.user,
		Username:   a.user,
		Source:     task.SourceUserAdded,
		Status:     task.StatusLogged,
	}

	// Attach the provider's key so the daemon can push the session upstream.
	if r, ok := a.remotes[name]; ok {
		t.Source = r.Provider
		t.ForeignKey = r.Key
		t.ForeignURL = r.URL
		if r.AssignedBy != "" {
			t.AssignedBy = r.AssignedBy
		}
	}

	if err := a.store.Save(t); err != nil {
		a.reportError("Saving task", err)
		return false
	}
	return true
}

// halt stops the clock without touching the store.
func (a *App) halt() {
	if !a.running {
		return
	}
	close(a.stopTick)
	a.running = false
}

// tick animates the elapsed-time display until stop is closed.
func (a *App) tick(stop <-chan struct{}, startedAt time.Time) {
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			a.dashboard.showElapsed(now.Sub(startedAt))
		}
	}
}

// SwitchTask stops whatever is running and starts timing another task. The tray
// menu and the Tasks page both drive this.
func (a *App) SwitchTask(name string) {
	if a.running {
		a.Stop()
	}
	a.dashboard.setTaskName(name)
	a.Start(name)
}

// Complete marks a task done, and Reopen puts it back in play.
func (a *App) Complete(id int, name string) {
	if err := a.store.Complete(id, name); err != nil {
		a.reportError("Completing task", err)
		return
	}
	a.refreshAfterWrite()
}

func (a *App) Reopen(id int, name string) {
	if err := a.store.Reopen(id); err != nil {
		a.reportError("Reopening task", err)
		return
	}
	a.dashboard.setTaskName(name)
	a.refreshAfterWrite()
}

// refreshAfterWrite reloads the visible page after a store write, even while the
// timer is running — which plain reload() refuses to do.
func (a *App) refreshAfterWrite() {
	tasks, err := a.store.Recent(recentLimit)
	if err != nil {
		a.reportError("Loading tasks", err)
		return
	}
	a.tasks = tasks

	a.reloadRemotes()
	a.nav[a.active].refresh()
	a.updateFooter()
	a.updateSystemTray()
}

// OpenRemote opens a provider-backed task in its web UI.
func (a *App) OpenRemote(name string) {
	r, ok := a.remotes[name]
	if !ok || r.URL == "" {
		return
	}

	parsed, err := url.Parse(r.URL)
	if err != nil {
		a.reportError("Opening task", err)
		return
	}
	if err := a.fyne.OpenURL(parsed); err != nil {
		a.reportError("Opening task", err)
	}
}

// ---------------------------------------------------------------------------
// Footer and tray
// ---------------------------------------------------------------------------

func (a *App) updateFooter() {
	total, count, err := a.store.TotalToday()
	if err != nil {
		a.reportError("Loading today's totals", err)
		return
	}

	target := a.workingDay()
	remaining := "Time remaining: " + clock(target-total)
	if total >= target {
		remaining = "Time over: " + clock(total-target)
	}

	setText(a.footTotal, "Total time today: "+clock(total))
	setText(a.footRemaining, remaining)
	setText(a.footTasks, fmt.Sprintf("Tasks today: %d", count))

	a.dashboard.showSummary(total, target, count)
}

// tickFooterClock drives the wall clock in the bottom-right corner.
func (a *App) tickFooterClock() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for now := range ticker.C {
		setText(a.footClock, now.Format(dateLayout+" "+clockLayout))
	}
}

func setText(t *canvas.Text, s string) {
	if t == nil || t.Text == s {
		return
	}
	t.Text = s
	t.Refresh()
}

// trayTaskLimit caps how many tasks the tray menu lists. Every open task plus
// everything pulled from a provider can run to dozens; a tray menu that long is
// unusable, so the rest stay reachable in the window.
const trayTaskLimit = 20

// updateSystemTray rebuilds the tray menu so any task can be started without
// opening the window: the running task's Stop control at the top, then every
// startable task — open local ones and everything pulled from a provider — each
// launching a timer, with today's accrued time shown when there is some.
func (a *App) updateSystemTray() {
	desk, ok := a.fyne.(desktop.App)
	if !ok {
		return
	}

	durations, err := a.store.DurationsToday()
	if err != nil {
		a.reportError("Loading tray totals", err)
		return
	}

	items := []*fyne.MenuItem{
		fyne.NewMenuItem("Open Task Timer", a.window.Show),
		fyne.NewMenuItem("Hide Task Timer", a.window.Hide),
		fyne.NewMenuItemSeparator(),
	}

	// The running task gets a Stop control at the top, so the tray both starts
	// and ends work rather than only starting it.
	if a.running {
		running := a.dashboard.taskName()
		items = append(items,
			fyne.NewMenuItem(fmt.Sprintf("Stop “%s”", running), func() { a.Stop() }),
			fyne.NewMenuItemSeparator(),
		)
	}

	names := a.taskOptions()
	if len(names) == 0 {
		empty := fyne.NewMenuItem("No tasks yet — add one in the app", nil)
		empty.Disabled = true
		items = append(items, empty)
	}

	for i, name := range names {
		if i >= trayTaskLimit {
			more := fyne.NewMenuItem(
				fmt.Sprintf("…and %d more in the app", len(names)-trayTaskLimit), nil)
			more.Disabled = true
			items = append(items, more)
			break
		}

		label := name
		if d, ok := durations[name]; ok && d > 0 {
			label = fmt.Sprintf("%s — %s", name, clock(d))
		}

		taskName := name
		item := fyne.NewMenuItem(label, func() { a.SwitchTask(taskName) })
		// A tick beside the task currently running, so the tray shows state as
		// well as offering actions.
		item.Checked = a.running && name == a.dashboard.taskName()
		items = append(items, item)
	}

	items = append(items,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Quit", a.fyne.Quit),
	)

	menu := fyne.NewMenu("Task Timer", items...)

	desk.SetSystemTrayMenu(menu)
	desk.SetSystemTrayIcon(appIcon())
	a.window.SetCloseIntercept(a.window.Hide)
}

// reportError surfaces a failure instead of killing the process. The original
// code called log.Fatal on every query error, so a transient SQLite lock — now
// routine, with the daemon writing concurrently — took the whole app down
// mid-session.
func (a *App) reportError(context string, err error) {
	log.Printf("%s: %v", context, err)
	if a.window != nil {
		dialog.ShowError(fmt.Errorf("%s: %w", context, err), a.window)
	}
}
