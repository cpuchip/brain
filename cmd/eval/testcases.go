package main

// getTestCases returns the curated evaluation dataset.
// Each test case has an input, expected outputs, and a focus area for filtering.
//
// Focus areas:
//   - "category"   — is the correct category assigned?
//   - "sub_items"  — are lists extracted into sub_items?
//   - "fields"     — are category-specific fields extracted?
//   - "tags"       — are relevant tags generated?
func getTestCases() []TestCase {
	return []TestCase{
		// ============================
		// CATEGORY tests
		// ============================
		{
			Name:           "simple action",
			Input:          "Pick up groceries after work",
			ExpectCategory: "actions",
			Focus:          "category",
		},
		{
			Name:            "person with context",
			Input:           "Sarah mentioned she's moving to Utah in March. She's looking for ward recommendations.",
			ExpectCategory:  "people",
			ExpectFieldName: "Sarah",
			Focus:           "category",
		},
		{
			Name:           "project with status",
			Input:          "Brain app: finished subtask CRUD, still need to add offline sync. Next step is implementing the queue.",
			ExpectCategory: "projects",
			Focus:          "category",
		},
		{
			Name:           "scripture study insight",
			Input:          "Alma 32:27 — faith as a seed. The experiment is the key. It's not about knowing, it's about being willing to try.",
			ExpectCategory: "study",
			Focus:          "category",
		},
		{
			Name:           "journal reflection",
			Input:          "Feeling grateful today. The kids were laughing at dinner and it just hit me how good life is right now.",
			ExpectCategory: "journal",
			Focus:          "category",
		},
		{
			Name:           "idea capture",
			Input:          "What if we built a scripture cross-reference graph? Like a knowledge graph but for the standard works.",
			ExpectCategory: "ideas",
			Focus:          "category",
		},
		{
			Name:           "ambiguous person vs action",
			Input:          "Call Bishop Johnson about the youth activity on Saturday",
			ExpectCategory: "actions",
			Focus:          "category",
		},
		{
			Name:            "person focus not action",
			Input:           "Bishop Johnson is really struggling right now. His mom is in hospice. He could use some meals brought in.",
			ExpectCategory:  "people",
			ExpectFieldName: "Bishop Johnson",
			Focus:           "category",
		},

		// ============================
		// SUB_ITEMS tests — list detection
		// ============================
		{
			Name:           "numbered shopping list",
			Input:          "Things to get at the store:\n1. Milk\n2. Bread\n3. Eggs\n4. Butter",
			ExpectCategory: "actions",
			ExpectSubItems: []string{"Milk", "Bread", "Eggs", "Butter"},
			MinSubItems:    4,
			Focus:          "sub_items",
		},
		{
			Name:           "bulleted todo list",
			Input:          "Weekend tasks:\n- Mow the lawn\n- Fix the fence\n- Clean the garage\n- Wash the car",
			ExpectSubItems: []string{"Mow the lawn", "Fix the fence", "Clean the garage", "Wash the car"},
			MinSubItems:    4,
			Focus:          "sub_items",
		},
		{
			Name:           "inline comma list",
			Input:          "Need to buy: milk, bread, eggs, and butter for the week",
			ExpectCategory: "actions",
			MinSubItems:    3,
			Focus:          "sub_items",
		},
		{
			Name:        "project steps list",
			Input:       "Steps to deploy the new version:\n1. Run tests\n2. Build Docker image\n3. Push to registry\n4. Update docker-compose\n5. Restart services",
			MinSubItems: 4,
			Focus:       "sub_items",
		},
		{
			Name:           "goals list",
			Input:          "Goals for this year:\n- Read the Book of Mormon cover to cover\n- Run a half marathon\n- Learn to play piano\n- Be more patient with the kids",
			MinSubItems:    4,
			ExpectSubItems: []string{"Book of Mormon", "half marathon", "piano", "patient"},
			Focus:          "sub_items",
		},
		{
			Name:           "no list — single thought",
			Input:          "I need to remember to call mom tomorrow about Sunday dinner",
			ExpectSubItems: []string{},
			Focus:          "sub_items",
		},
		{
			Name:           "study references list",
			Input:          "Scriptures on faith:\n- Alma 32:21\n- Hebrews 11:1\n- Ether 12:6\n- Moroni 7:33",
			ExpectCategory: "study",
			MinSubItems:    4,
			Focus:          "sub_items",
		},
		{
			Name:        "mixed list with context",
			Input:       "For family home evening this week, here's what we need:\n1. Opening song picked out\n2. Someone to give the lesson on gratitude\n3. Treats — maybe brownies?\n4. Activity — board game night",
			MinSubItems: 3,
			Focus:       "sub_items",
		},
		{
			Name:        "dash-separated list",
			Input:       "Packing list for camping trip:\n- Tent\n- Sleeping bags\n- Cooler with food\n- Firewood\n- Matches\n- First aid kit",
			MinSubItems: 5,
			Focus:       "sub_items",
		},
		{
			Name:        "numbered steps with detail",
			Input:       "How to set up the new brain server:\n1. Install Go 1.21+\n2. Clone the brain repo to scripts/brain\n3. Copy .env.example to .env and configure\n4. Run go build -o brain.exe ./cmd/brain\n5. Start with ./brain.exe",
			MinSubItems: 4,
			Focus:       "sub_items",
		},

		// ============================
		// FIELDS tests
		// ============================
		{
			Name:            "person name extraction",
			Input:           "Met with Elder Holland after the fireside. He shared a powerful thought about enduring to the end.",
			ExpectCategory:  "people",
			ExpectFieldName: "Elder Holland",
			Focus:           "fields",
		},
		{
			Name:           "action with due date",
			Input:          "Submit the quarterly report by 2026-03-15",
			ExpectCategory: "actions",
			Focus:          "fields",
		},
		{
			Name:           "study with references",
			Input:          "The connection between D&C 93:36 and Abraham 3:19 on intelligence is fascinating. Light and truth.",
			ExpectCategory: "study",
			Focus:          "fields",
		},

		// ============================
		// TAGS tests
		// ============================
		{
			Name:           "scripture tags",
			Input:          "Studying the Sermon on the Mount today. Matthew 5-7 is dense with covenant language.",
			ExpectCategory: "study",
			ExpectTags:     []string{"scripture"},
			Focus:          "tags",
		},
		{
			Name:       "family tags",
			Input:      "Need to plan the family reunion this summer. Probably at the cabin again.",
			ExpectTags: []string{"family"},
			Focus:      "tags",
		},
	}
}
