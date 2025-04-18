package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

func Fantasy(mux *http.ServeMux) {
	mux.HandleFunc("/fantasy", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(FantasyFullIndexPage().Render()))
	})

	mux.HandleFunc("/fantasy/sheet", func(w http.ResponseWriter, r *http.Request) {
		all := getAllFantasyCards()
		w.Write([]byte(FantasyCardSheetsPage(all).Render()))
	})

	mux.HandleFunc("/fantasy/screenshot", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("Generating screenshots...")
		go generateSheetScreenshots() // run async
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": true}`))
	})

	mux.Handle("/fantasy/sheet/screens/", http.StripPrefix("/fantasy/sheet/screens/", http.FileServer(http.Dir("./data/fantasy_sheets"))))

	mux.HandleFunc("/fantasy/sheet/download", func(w http.ResponseWriter, r *http.Request) {
		zipPath := "./data/fantasy_sheets.zip"
	
		// Always regenerate the zip to ensure it's fresh
		err := zipFolder("./data/fantasy_sheets", zipPath)
		if err != nil {
			http.Error(w, "Failed to zip files", http.StatusInternalServerError)
			fmt.Println("Failed to generate zip:", err)
			return
		}
	
		// Set headers to force download
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", "attachment; filename=\"fantasy_sheets.zip\"")
	
		http.ServeFile(w, r, zipPath)
	})
	
}

func generateSheetScreenshots() {
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	dir := "./data/fantasy_sheets"
	_ = os.MkdirAll(dir, os.ModePerm)

	// Fetch rendered HTML of the sheet view
	resp, err := http.Get("http://localhost:8080/fantasy/sheet")
	if err != nil {
		fmt.Println("Error fetching /fantasy/sheet:", err)
		return
	}
	htmlBuf, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		fmt.Println("Error reading response body:", err)
		return
	}

	tmpHtmlPath := filepath.Join(dir, "sheet.html")
	_ = os.WriteFile(tmpHtmlPath, htmlBuf, 0644)

	// Navigate and query
	fileURL := "http://localhost:8080/fantasy/sheet"

	var nodes []*cdp.Node
	err = chromedp.Run(ctx,
		chromedp.Navigate(fileURL),
		chromedp.Nodes(".print-sheet", &nodes),
	)
	if err != nil {
		fmt.Println("Error querying .print-sheet nodes:", err)
		return
	}

	for i, n := range nodes {
		outPath := filepath.Join(dir, fmt.Sprintf("sheet-%02d.png", i+1))

		// Skip if already exists
		if _, err := os.Stat(outPath); err == nil {
			fmt.Println("Already exists, skipping:", outPath)
			continue
		}

		var buf []byte
		err := chromedp.Run(ctx,
			chromedp.Screenshot(n.FullXPath(), &buf, chromedp.NodeVisible),
		)
		if err != nil {
			fmt.Printf("Screenshot %d failed: %v\n", i, err)
			continue
		}
		_ = os.WriteFile(outPath, buf, 0644)
		fmt.Println("Saved screenshot:", outPath)
	}
}

func getAllFantasyCards() []FantasyCard {
	return append(
		append(
			append(
				append(
					append(
						append(
							append(
								append(
									append(
										append(
											races.Cards,
											classes.Cards...),
										subclasses.Cards...),
									attacks.Cards...),
								defends.Cards...),
							negotiates.Cards...),
						spells.Cards...),
					myths.Cards...),
				goals.Cards...),
			locations.Cards...),
		items.Cards...)
}

func FantasyFullIndexPage() *Node {
	sections := []struct {
		Title string
		Cards []FantasyCard
		Style string
	}{
		{"Races", races.Cards, "bg-green-100 border-green-400"},
		{"Classes", classes.Cards, "bg-violet-100 border-violet-400"},
		{"Subclasses", subclasses.Cards, "bg-orange-100 border-orange-400"},
		{"Attacks", attacks.Cards, "bg-rose-100 border-rose-400"},
		{"Defends", defends.Cards, "bg-blue-100 border-blue-400"},
		{"Negotiates", negotiates.Cards, "bg-purple-100 border-purple-400"},
		{"Spells", spells.Cards, "bg-yellow-100 border-yellow-400"},
		{"Myths", myths.Cards, "bg-gray-100 border-gray-400"},
		{"Goals", goals.Cards, "bg-pink-100 border-pink-400"},
		{"Locations", locations.Cards, "bg-cyan-100 border-cyan-400"},
		{"Items", items.Cards, "bg-lime-100 border-lime-400"},
	}

	return DefaultLayout(
		Div(Class("container mx-auto p-4 space-y-12"),
			H1(Class("text-3xl font-bold mb-8"), T("Fantasy Card Library")),
			Ch(func() []*Node {
				var out []*Node
				for _, sec := range sections {
					out = append(out,
						Div(Class("space-y-4"),
							H2(Class("text-xl font-semibold"), T(sec.Title)),
							Div(Class("grid grid-cols-2 sm:grid-cols-4 lg:grid-cols-6 gap-4"),
								Ch(func(cards []FantasyCard, style string) []*Node {
									nodes := []*Node{}
									for _, c := range cards {
										nodes = append(nodes, FantasyCardFace(c, "", true))
									}
									return nodes
								}(sec.Cards, sec.Style)),
							),
						),
					)
				}
				return out
			}()),
		),
	)
}

func FantasyCardSheetsPage(all []FantasyCard) *Node {
	sheets := []*Node{}
	for i := 0; i < len(all); i += 8 {
		end := i + 8
		if end > len(all) {
			end = len(all)
		}
		sheets = append(sheets, FantasyCardSheet(all[i:end]))
	}

	return DefaultLayout(
		Div(Class("flex flex-col items-center"),
			H1(Class("text-2xl font-bold my-4"), T("Fantasy Print Sheets")),
			Button(
				Class("btn btn-primary my-4 no-print"),
				OnClick(`fetch('/fantasy/screenshot')
			  .then(() => setTimeout(() => { window.location.href = '/fantasy/sheet/download'; }, 1500))`),
				T("Download All Sheets (PNG ZIP)"),
			),
			Ch(sheets),
			A(Href("/fantasy"), Class("btn btn-secondary mt-4 no-print"), T("Back")),
		),
	)
}

func FantasyCardSheet(cards []FantasyCard) *Node {
	for len(cards) < 8 {
		cards = append(cards, FantasyCard{}) // empty placeholders
	}
	panels := []*Node{}
	for _, c := range cards {
		panel := Div(Class("flex justify-center items-center w-full h-full"))
		if c.Title != "" {
			panel.Children = append(panel.Children, FantasyCardFace(c, "", false))
		}
		panels = append(panels, panel)
	}
	return Div(Class("print-sheet grid grid-cols-4 grid-rows-2 gap-0 border border-gray-300 mb-4"),
		Ch(panels),
	)
}

func FantasyCardFace(c FantasyCard, _ string, thumb bool) *Node {
	style := fantasyStyleMap[c.Category]
	size := "w-40"
	titleSize := "text-lg"
	descSize := "text-sm"
	pad := "p-4"
	if thumb {
		size = "w-28"
		titleSize = "text-sm"
		descSize = "text-xs"
		pad = "p-2"
	}
	return Div(Class(size+" aspect-[2.5/3.5] border-2 rounded-lg shadow-md "+style+" text-black flex flex-col justify-center "+pad+" text-center space-y-2"),
		Div(Class(titleSize+" font-bold"), T(c.Title)),
		Div(Class(descSize+" whitespace-pre-wrap break-words"), T(c.Text)),
	)
}

func zipFolder(sourceDir, zipPath string) error {
	fmt.Println("Creating ZIP file at:", zipPath)

	// Open zip file for writing
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("could not create zip file: %w", err)
	}
	defer zipFile.Close()

	// Create archive writer
	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	// Walk sourceDir to find all .png files
	err = filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(d.Name()) != ".png" {
			fmt.Println("Skipping non-png file:", d.Name())
			return nil
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("error getting relative path: %w", err)
		}

		fmt.Println("Zipping:", relPath)
		fileData, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("error reading file: %w", err)
		}

		writer, err := archive.Create(relPath)
		if err != nil {
			return fmt.Errorf("error creating entry in zip: %w", err)
		}

		_, err = writer.Write(fileData)
		return err
	})

	if err != nil {
		fmt.Println("ZIP creation failed:", err)
		return err
	}

	fmt.Println("✅ ZIP creation completed")
	return nil
}




type FantasyCard struct {
	Title    string
	Text     string
	Category string
}

type Race struct {
	Cards []FantasyCard
}

type CardClass struct {
	Cards []FantasyCard
}

type Subclass struct {
	Cards []FantasyCard
}

type Attack struct {
	Cards []FantasyCard
}

type Defend struct {
	Cards []FantasyCard
}

type Negotiate struct {
	Cards []FantasyCard
}

type Spell struct {
	Cards []FantasyCard
}

type Myth struct {
	Cards []FantasyCard
}

type Goal struct {
	Cards []FantasyCard
}

type Location struct {
	Cards []FantasyCard
}

type Item struct {
	Cards []FantasyCard
}

var fantasyStyleMap = map[string]string{
	"Race":      "bg-green-100 border-green-400",
	"Class":     "bg-violet-100 border-violet-400",
	"Subclass":  "bg-orange-100 border-orange-400",
	"Attack":    "bg-rose-100 border-rose-400",
	"Defend":    "bg-blue-100 border-blue-400",
	"Negotiate": "bg-purple-100 border-purple-400",
	"Spell":     "bg-yellow-100 border-yellow-400",
	"Myth":      "bg-gray-100 border-gray-400",
	"Goal":      "bg-pink-100 border-pink-400",
	"Location":  "bg-cyan-100 border-cyan-400",
	"Item":      "bg-lime-100 border-lime-400",
}

var races = Race{
	Cards: []FantasyCard{
		{"Elf", "+1 Spell Power. Peek at the top Rule card each round.", "Race"},
		{"Dwarf", "+1 Defense. Recover 1 Health when you lose a trick.", "Race"},
		{"Orc", "+2 Attack. Lose 1 Health unless you win a trick each round.", "Race"},
		{"Human", "+1 to all stats. Adaptable: copy one ability per round.", "Race"},
		{"Dragonkin", "+1 Spell Power. Immune to fire-based effects.", "Race"},
		{"Halfling", "Immune to damage if you have taken 0 tricks.", "Race"},
	},
}

var classes = CardClass{
	Cards: []FantasyCard{
		{"Warrior", "Win ties. +1 Health if you take the most tricks this round.", "Class"},
		{"Wizard", "Spells cost 1 less. Cast instead of playing once per round.", "Class"},
		{"Rogue", "Change a card after others have played (once per game).", "Class"},
		{"Cleric", "Heal 1 Health after every third trick won.", "Class"},
		{"Bard", "Each trick won gives +1 to Spell next round.", "Class"},
	},
}

var subclasses = Subclass{
	Cards: []FantasyCard{
		{"Fire Mage", "+2 Spell Power. Deal 2 damage on a Heart win.", "Subclass"},
		{"Paladin", "Heal allies when you heal. Cannot be the first to fall.", "Subclass"},
		{"Assassin", "+1 Attack vs lowest-health player. Win ties if attacking them.", "Subclass"},
		{"Elementalist", "Spells change based on suit: Fire (Hearts), Ice (Clubs), Wind (Spades), Earth (Diamonds).", "Subclass"},
	},
}

var attacks = Attack{
	Cards: []FantasyCard{
		{"Strike", "Deal 1 damage to the player you beat in a trick.", "Attack"},
		{"Cleave", "Deal 1 damage to two players if you win two tricks in a row.", "Attack"},
		{"Ambush", "Declare a target. If you win the next trick, deal 2 damage to them.", "Attack"},
		{"Critical Hit", "Win with a face card to deal +2 damage.", "Attack"},
		{"Volley", "You may target any player within 1 table seat. Deal 1 damage if you win the trick.", "Attack"},
	},
}

var defends = Defend{
	Cards: []FantasyCard{
		{"Shield", "Negate one incoming Attack or Spell.", "Defend"},
		{"Counterspell", "Cancel a Spell played immediately after yours.", "Defend"},
		{"Barrier", "Prevent all damage for one round.", "Defend"},
		{"Reflect", "Send the next attack targeting you back at the attacker.", "Defend"},
		{"Last Stand", "If reduced to 0 Health this round, stay alive with 1.", "Defend"},
	},
}

var negotiates = Negotiate{
	Cards: []FantasyCard{
		{"Truce", "Skip trick effects with one chosen player this round.", "Negotiate"},
		{"Alliance", "Form a team for one round. Share Health and trick rewards.", "Negotiate"},
		{"Bargain", "Trade 1 Health to gain 1 point or draw 1 card.", "Negotiate"},
		{"Sabotage", "Force a player to discard a card if you win a trick against them.", "Negotiate"},
	},
}

var spells = Spell{
	Cards: []FantasyCard{
		{"Lightning Bolt", "Win with Spades: remove 1 card from a player's hand.", "Spell"},
		{"Heal", "Restore 2 Health to yourself or another player.", "Spell"},
		{"Fireball", "Deal 1 damage to all other players.", "Spell"},
		{"Teleport", "Swap cards with another player before tricks begin.", "Spell"},
		{"Silence", "Cancel another player's Class or Race ability this round.", "Spell"},
	},
}

var myths = Myth{
	Cards: []FantasyCard{
		{"Eclipse", "All Spells cost double to cast this round.", "Myth"},
		{"Dragon's Flight", "Highest Health player becomes target. All others gain +1 Attack.", "Myth"},
		{"Ancient Prophecy", "Reveal top 3 cards of the deck. Apply 1 immediately.", "Myth"},
		{"Blood Moon", "All damage is doubled.", "Myth"},
		{"Divine Intervention", "Lowest Health player heals to full.", "Myth"},
		{"Crown of the Forgotten King", "The leader this round cannot take damage but must give away all trick rewards.", "Myth"},
		{"Whispers of the Deep", "The player with the most Spell cards loses 2 Health as madness creeps in.", "Myth"},
		{"Crimson Comet", "Deal 3 damage to the trick loser this round.", "Myth"},
		{"Rising Tides", "Each player discards one card unless they won a trick last round.", "Myth"},
		{"Chained Gods", "No player may heal or use Myth cards this round.", "Myth"},
		{"Shattered Veil", "All players swap hands with the opponent to their left. No cards may be viewed until the next trick.", "Myth"},
		{"Storm of Echoes", "Each Spell cast this round is duplicated on a random target.", "Myth"},
	},
}

var goals = Goal{
	Cards: []FantasyCard{
		{"Slayer", "Win if you reduce all others to 0 Health.", "Goal"},
		{"Seeker", "Win if you collect 3 Location cards via Diamonds tricks.", "Goal"},
		{"Healer", "Win if you end with the highest Health and at least 5 tricks taken.", "Goal"},
		{"Guardian", "Win if you protect an ally and survive 5 rounds together.", "Goal"},
		{"Betrayer", "Win if you eliminate a teammate and survive 3 more rounds.", "Goal"},
		{"Prophet", "Win if you predict and match the winner of each round for 3 consecutive rounds.", "Goal"},
		{"Collector", "Win if you hold at least one card from each category by game's end.", "Goal"},
		{"Herald of Change", "Win if you cause three different Myth effects to trigger.", "Goal"},
		{"Pilgrim", "Win if you play in 4 unique Locations and survive to the end.", "Goal"},
		{"Champion of Light", "Win if you end the game with full Health and no attacks used.", "Goal"},
		{"Echo of Legends", "Win if you use the same card title to win three different tricks across the game.", "Goal"},
		{"Silent Blade", "Win if you deal the final blow to two different players in the same round.", "Goal"},
	},
}

var locations = Location{
	Cards: []FantasyCard{
		{"Dark Forest", "All players lose 1 Health unless they win a trick.", "Location"},
		{"Sacred Grove", "Healing is doubled. Spells may not be cast.", "Location"},
		{"Battlefield", "+1 Attack for all. Trump rules ignored.", "Location"},
		{"Crystal Cavern", "Players may peek at one opponent's card before play.", "Location"},
		{"Wizards' Tower", "Spells cost 1 less. Draw an extra Spell if you win a trick with Hearts.", "Location"},
		{"Ruined Citadel", "Players cannot use Defend cards. +1 point for winning with Kings or Queens.", "Location"},
		{"Frozen Wastes", "Players may only play Clubs or take 1 damage each round.", "Location"},
		{"Infernal Rift", "+1 Spell Power, but every trick won costs 1 Health.", "Location"},
		{"Golden Market", "Players may trade cards once per round before play.", "Location"},
		{"Sky Temple", "First player to win a trick here draws a Goal card.", "Location"},
		{"Thornspine Hollow", "Any player who wins with a Heart card takes 1 damage from hidden brambles.", "Location"},
		{"Mirror Lake", "Any ability used here affects both the user and their target equally.", "Location"},
	},
}

var items = Item{
	Cards: []FantasyCard{
		{"Sword of Embers", "+1 Attack. Must win a trick with a Heart to claim.", "Item"},
		{"Cloak of Shadows", "Ignore the first attack against you. Must win a trick with a Jack.", "Item"},
		{"Amulet of Echoes", "Reuse a Spell once per game. Must play two Spells in one round to claim.", "Item"},
		{"Tome of the Ancients", "+1 Spell Power. Must win a trick with a King in the Wizards' Tower.", "Item"},
		{"Boots of Swiftness", "Always play first. Claim after winning 3 tricks in one round.", "Item"},
		{"Ring of Binding", "Negate one Myth effect. Must win a trick during a Myth event.", "Item"},
		{"Shield of the Watcher", "+1 Defense. Claim if you prevent damage for another player.", "Item"},
		{"Wand of Forked Light", "Target two players with one Spell. Must cast a Spell while at 1 Health.", "Item"},
		{"Elixir of Resolve", "Gain 3 Health instantly. Claim if you play no attacks for two rounds.", "Item"},
		{"Orb of Forgotten Time", "Skip your turn once per game. Claim if you lose a trick with a Queen.", "Item"},
	},
}
