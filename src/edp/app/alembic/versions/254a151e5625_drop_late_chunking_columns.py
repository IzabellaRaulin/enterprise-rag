# Copyright (C) 2026 Intel Corporation
# SPDX-License-Identifier: Apache-2.0
"""Drop late chunking columns

Revision ID: 254a151e5625
Revises: c7d8e9f0a1b2
Create Date: 2026-11-04 12:00:00.000000

"""
from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa


# revision identifiers, used by Alembic.
revision: str = '254a151e5625'
down_revision: Union[str, Sequence[str], None] = 'c7d8e9f0a1b2'
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    """Upgrade schema."""
    op.drop_column('links', 'late_chunking_end')
    op.drop_column('links', 'late_chunking_start')
    op.drop_column('files', 'late_chunking_end')
    op.drop_column('files', 'late_chunking_start')


def downgrade() -> None:
    """Downgrade schema."""
    op.add_column('files', sa.Column('late_chunking_start', sa.DateTime(), nullable=True))
    op.add_column('files', sa.Column('late_chunking_end', sa.DateTime(), nullable=True))
    op.add_column('links', sa.Column('late_chunking_start', sa.DateTime(), nullable=True))
    op.add_column('links', sa.Column('late_chunking_end', sa.DateTime(), nullable=True))
