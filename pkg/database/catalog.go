package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"

	"log/slog"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"github.com/grafana/quickpizza/pkg/database/migrations"
	"github.com/grafana/quickpizza/pkg/errorinjector"
	"github.com/grafana/quickpizza/pkg/model"
	"github.com/grafana/quickpizza/pkg/password"
)

type Catalog struct {
	db *bun.DB

	fixedPizzas  int
	fixedUsers   int
	fixedRatings int
	maxPizzas    int
	maxUsers     int
	maxRatings   int
}

func NewCatalog(connString string) (*Catalog, error) {
	db, err := initializeDB(connString)
	if err != nil {
		return nil, err
	}
	log := slog.With("db", "catalog")
	mig := migrate.NewMigrator(db, migrations.Catalog)
	if err := mig.Init(context.Background()); err != nil {
		return nil, err
	}
	log.Info("running migrations")
	g, err := mig.Migrate(context.Background())
	log.Info("applied migrations", "count", len(g.Migrations.Applied()))
	if err != nil {
		return nil, err
	}
	db.RegisterModel((*model.PizzaToIngredients)(nil))

	c := &Catalog{
		db:           db,
		fixedPizzas:  envInt("QUICKPIZZA_DB_FIXED_PIZZAS", 100),
		fixedUsers:   envInt("QUICKPIZZA_DB_FIXED_USERS", 10),
		fixedRatings: envInt("QUICKPIZZA_DB_FIXED_RATINGS", 10),
		maxPizzas:    envInt("QUICKPIZZA_DB_MAX_PIZZAS", 5000),
		maxUsers:     envInt("QUICKPIZZA_DB_MAX_USERS", 5000),
		maxRatings:   envInt("QUICKPIZZA_DB_MAX_RATINGS", 10000),
	}

	log.Info(
		"Catalog parameters",
		"fixedPizzas", c.fixedPizzas,
		"fixedUsers", c.fixedUsers,
		"fixedRatings", c.fixedRatings,
		"maxPizzas", c.maxPizzas,
		"maxUsers", c.maxUsers,
		"maxRatings", c.maxRatings,
	)

	return c, nil
}

func (c *Catalog) GetIngredients(ctx context.Context, t string) ([]model.Ingredient, error) {
	// Inject an artificial error for testing purposes
	err := errorinjector.InjectErrors(ctx, "get-ingredients")
	if err != nil {
		return nil, err
	}

	var ingredients []model.Ingredient
	err = c.db.NewSelect().Model(&ingredients).Where("type = ?", t).Scan(ctx)
	return ingredients, err
}

func (c *Catalog) GetDoughs(ctx context.Context) ([]model.Dough, error) {
	var doughs []model.Dough
	err := c.db.NewSelect().Model(&doughs).Scan(ctx)
	return doughs, err
}

func (c *Catalog) GetTools(ctx context.Context) ([]string, error) {
	var tools []string
	err := c.db.NewSelect().Model(&model.Tool{}).Column("name").Scan(ctx, &tools)
	return tools, err
}

func (c *Catalog) GetHistory(ctx context.Context, limit int) ([]model.Pizza, error) {
	var history []model.Pizza
	err := c.db.NewSelect().Model(&history).Relation("Dough").Relation("Ingredients").Order("created_at DESC").Limit(limit).Scan(ctx)
	return history, err
}

func (c *Catalog) GetRecommendation(ctx context.Context, id int) (*model.Pizza, error) {
	var pizza model.Pizza
	err := c.db.NewSelect().Model(&pizza).Relation("Dough").Relation("Ingredients").Where("pizza.id = ?", id).Limit(1).Scan(ctx)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &pizza, err
}

func (c *Catalog) RecordUser(ctx context.Context, user *model.User) error {
	passwordHash, err := password.HashPassword(user.Password)
	if err != nil {
		return err
	}

	user.PasswordHash = passwordHash
	user.Token = model.GenerateUserToken()

	return c.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewInsert().Model(user).Exec(ctx)
		if err != nil {
			return err
		}

		return c.enforceTableSizeLimits(ctx, tx, (*model.User)(nil), c.fixedUsers, c.maxUsers)
	})
}

func (c *Catalog) LoginUser(ctx context.Context, username, passwordText string) (*model.User, error) {
	var user model.User
	err := c.db.NewSelect().Model(&user).Where("username = ?", username).Limit(1).Scan(ctx)
	if err == sql.ErrNoRows {
		return nil, nil
	}

	if password.CheckPassword(passwordText, user.PasswordHash) {
		return &user, nil
	}
	return nil, nil
}

func (c *Catalog) RecordRecommendation(ctx context.Context, pizza *model.Pizza) error {
	// Inject an artificial error for testing purposes
	err := errorinjector.InjectErrors(ctx, "record-recommendation")
	if err != nil {
		return err
	}

	pizza.DoughID = pizza.Dough.ID
	return c.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewInsert().Model(pizza).Exec(ctx)
		if err != nil {
			return err
		}
		for _, i := range pizza.Ingredients {
			_, err = tx.NewInsert().Model(&model.PizzaToIngredients{PizzaID: pizza.ID, IngredientID: i.ID}).Exec(ctx)
			if err != nil {
				return err
			}
		}

		return c.enforceTableSizeLimits(ctx, tx, (*model.Pizza)(nil), c.fixedPizzas, c.maxPizzas)
	})
}

// enforceTableSizeLimits limits the size of a table, which must have an ID row.
// All rows will be deleted except the N newest ones, where N == maximum.
// If fixed > 0, then the first K rows (IDs 0, 1, 2...) will never be deleted,
// where K == fixed (even if this would make the table exceed N rows).
// If maximum is 0 or negative, then do not enforce any limits.
// Useful for keeping an in-memory SQLite database size below a certain number.
func (c *Catalog) enforceTableSizeLimits(ctx context.Context, tx bun.Tx, model any, fixed, maximum int) error {
	if maximum <= 0 {
		return nil
	}
	_, err := tx.NewDelete().
		Model(model).
		Where(fmt.Sprintf("id NOT IN (?) AND id > %v", fixed), tx.NewSelect().
			Model(model).
			Order("created_at DESC").
			Column("id").
			Limit(maximum)).
		Exec(ctx)
	return err
}

func envInt(name string, defaultVal int) int {
	v, found := os.LookupEnv(name)
	if !found {
		return defaultVal
	}

	b, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}

	return b
}
