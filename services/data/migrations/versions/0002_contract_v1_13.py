"""Synchronize issue model with internal API contract v1.13.

Revision ID: 0002_contract_v1_13
Revises: 0001
"""

import sqlalchemy as sa
from alembic import op

revision = "0002_contract_v1_13"
down_revision = "0001"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("issues", sa.Column("assigned_to", sa.UUID(), nullable=True))
    op.create_foreign_key(
        op.f("fk_issues_assigned_to_employees"), "issues", "employees", ["assigned_to"], ["id"],
        ondelete="RESTRICT",
    )
    op.drop_constraint(op.f("ck_issues_stage"), "issues", type_="check")
    op.create_check_constraint(
        op.f("ck_issues_stage"), "issues", "stage IN ('before', 'during', 'return', 'after', 'post_return')",
    )
    op.drop_constraint(op.f("ck_issues_category"), "issues", type_="check")
    op.create_check_constraint(
        op.f("ck_issues_category"), "issues",
        "category IN ('body_damage', 'mechanical', 'cleanliness', 'keys', 'parking', 'car_lock', 'other')",
    )


def downgrade() -> None:
    op.drop_constraint(op.f("ck_issues_category"), "issues", type_="check")
    op.create_check_constraint(
        op.f("ck_issues_category"), "issues", "category IN ('body_damage', 'mechanical', 'cleanliness', 'keys', 'other')",
    )
    op.drop_constraint(op.f("ck_issues_stage"), "issues", type_="check")
    op.create_check_constraint(
        op.f("ck_issues_stage"), "issues", "stage IN ('before', 'during', 'return', 'after')",
    )
    op.drop_constraint(op.f("fk_issues_assigned_to_employees"), "issues", type_="foreignkey")
    op.drop_column("issues", "assigned_to")
