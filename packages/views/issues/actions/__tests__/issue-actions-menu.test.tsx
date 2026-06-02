import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Issue } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enIssues from "../../../locales/en/issues.json";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

// ---------------------------------------------------------------------------
// Mocks — same pattern as the issue-detail test suite.
// ---------------------------------------------------------------------------

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

const mockOpenModal = vi.fn();
vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(
    (selector?: any) => {
      const state = { open: mockOpenModal };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ open: mockOpenModal }) },
  ),
}));

const mockAuthState = { user: { id: "user-1" }, isAuthenticated: true };
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: any) => (selector ? selector(mockAuthState) : mockAuthState),
    { getState: () => mockAuthState },
  ),
  registerAuthStore: vi.fn(),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "members"],
    queryFn: () =>
      Promise.resolve([
        { user_id: "user-1", name: "Test User", email: "t@t.com", role: "admin" },
      ]),
  }),
  agentListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "agents"],
    queryFn: () => Promise.resolve([]),
  }),
  squadListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "squads"],
    queryFn: () => Promise.resolve([]),
  }),
  assigneeFrequencyOptions: () => ({
    queryKey: ["workspaces", "ws-1", "assignee-frequency"],
    queryFn: () => Promise.resolve([]),
  }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_t: string, _id: string) => "" }),
}));

vi.mock("@multica/core/pins", () => ({
  pinListOptions: () => ({
    queryKey: ["pins", "ws-1", "user-1"],
    queryFn: () => Promise.resolve([]),
  }),
  useCreatePin: () => ({ mutate: vi.fn() }),
  useDeletePin: () => ({ mutate: vi.fn() }),
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useUpdateIssue: () => ({ mutate: vi.fn() }),
}));

vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>(
    "@multica/core/paths",
  );
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", name: "Test", slug: "test" }),
    useWorkspacePaths: () => actual.paths.workspace("test"),
  };
});

// Mutable so individual tests can simulate the desktop shell (current path +
// the desktop-only `openInNewTab` adapter method) for the "Open in new tab" item.
const navMock: {
  push: ReturnType<typeof vi.fn>;
  pathname: string;
  searchParams: URLSearchParams;
  back: ReturnType<typeof vi.fn>;
  replace: ReturnType<typeof vi.fn>;
  openInNewTab?: ReturnType<typeof vi.fn>;
} = {
  push: vi.fn(),
  pathname: "/test/issues/issue-1",
  searchParams: new URLSearchParams(),
  back: vi.fn(),
  replace: vi.fn(),
  openInNewTab: undefined,
};
vi.mock("../../../navigation", () => ({
  useNavigation: () => navMock,
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("../../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: any) => <span data-testid="actor">{actorId}</span>,
}));

// Import after mocks.
import { IssueActionsDropdown } from "../issue-actions-dropdown";
import { IssueActionsContextMenu } from "../issue-actions-context-menu";

const mockIssue: Issue = {
  id: "issue-1",
  workspace_id: "ws-1",
  number: 1,
  identifier: "TES-1",
  title: "Example",
  description: null,
  status: "todo",
  priority: "medium",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  start_date: null,
  due_date: null,
  project_id: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
} as Issue;

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={qc}>{ui}</QueryClientProvider>
    </I18nProvider>
  );
}

beforeEach(() => {
  mockOpenModal.mockReset();
  navMock.push.mockReset();
  navMock.pathname = "/test/issues/issue-1";
  navMock.openInNewTab = undefined;
  delete (window as unknown as { desktopAPI?: unknown }).desktopAPI;
});

/** Stub the preload bridge so `isDesktopShell()` returns true. */
function enterDesktopShell() {
  (window as unknown as { desktopAPI?: unknown }).desktopAPI = {
    pickDirectory: vi.fn(),
  };
}

describe("IssueActionsDropdown", () => {
  it("renders the top-level items when the trigger is clicked", async () => {
    render(
      wrap(
        <IssueActionsDropdown
          issue={mockIssue}
          trigger={<button data-testid="trigger">Menu</button>}
        />,
      ),
    );

    fireEvent.click(screen.getByTestId("trigger"));

    // Base UI portals the popup; role=menu lands on the popup wrapper.
    expect(await screen.findByText("Status")).toBeInTheDocument();
    expect(screen.getByText("Priority")).toBeInTheDocument();
    expect(screen.getByText("Assignee")).toBeInTheDocument();
    expect(screen.getByText("Due date")).toBeInTheDocument();
    expect(screen.getByText("Copy link")).toBeInTheDocument();
    expect(screen.getByText("More")).toBeInTheDocument();
    expect(screen.getByText("Delete issue")).toBeInTheDocument();
    // "Open in new tab" is desktop-only; absent on web / outside the shell.
    expect(screen.queryByText("Open in new tab")).not.toBeInTheDocument();
    // Relationship actions are hidden inside the "More" submenu by default.
    expect(screen.queryByText("Create sub-issue")).not.toBeInTheDocument();
    expect(screen.queryByText("Set parent issue...")).not.toBeInTheDocument();
    expect(screen.queryByText("Add sub-issue...")).not.toBeInTheDocument();
  });

  it("shows 'Open in new tab' on desktop from a different path and opens a foreground tab", async () => {
    enterDesktopShell();
    navMock.openInNewTab = vi.fn();
    navMock.pathname = "/test/issues"; // list view, not this issue's own tab
    render(
      wrap(
        <IssueActionsDropdown
          issue={mockIssue}
          trigger={<button data-testid="trigger">Menu</button>}
        />,
      ),
    );

    fireEvent.click(screen.getByTestId("trigger"));
    fireEvent.click(await screen.findByText("Open in new tab"));

    expect(navMock.openInNewTab).toHaveBeenCalledWith(
      "/test/issues/issue-1",
      undefined,
      { activate: true },
    );
  });

  it("hides 'Open in new tab' on the issue's own detail tab (target === current path)", async () => {
    enterDesktopShell();
    navMock.openInNewTab = vi.fn();
    navMock.pathname = "/test/issues/issue-1";
    render(
      wrap(
        <IssueActionsDropdown
          issue={mockIssue}
          trigger={<button data-testid="trigger">Menu</button>}
        />,
      ),
    );

    fireEvent.click(screen.getByTestId("trigger"));
    await screen.findByText("Copy link");
    expect(screen.queryByText("Open in new tab")).not.toBeInTheDocument();
  });

  it("clicking the Assignee item opens the shared AssigneePicker popover", async () => {
    render(
      wrap(
        <IssueActionsDropdown
          issue={mockIssue}
          trigger={<button data-testid="trigger">Menu</button>}
        />,
      ),
    );

    fireEvent.click(screen.getByTestId("trigger"));
    fireEvent.click(await screen.findByText("Assignee"));

    // The shared picker exposes a search input and renders the workspace
    // member under a "Members" group — both come from `AssigneePicker`, not
    // the legacy submenu (which had neither).
    expect(
      await screen.findByPlaceholderText("Assign to..."),
    ).toBeInTheDocument();
    expect(await screen.findByText("Members")).toBeInTheDocument();
    expect(await screen.findByText("Test User")).toBeInTheDocument();
  });

  it("clicking Delete issue opens the delete-confirm modal", async () => {
    render(
      wrap(
        <IssueActionsDropdown
          issue={mockIssue}
          trigger={<button data-testid="trigger">Menu</button>}
          onDeletedNavigateTo="/test/issues"
        />,
      ),
    );

    fireEvent.click(screen.getByTestId("trigger"));
    const del = await screen.findByText("Delete issue");
    fireEvent.click(del);

    expect(mockOpenModal).toHaveBeenCalledWith("issue-delete-confirm", {
      issueId: "issue-1",
      identifier: "TES-1",
      onDeletedNavigateTo: "/test/issues",
    });
  });
});

describe("IssueActionsContextMenu", () => {
  it("renders the menu when the wrapped element receives a contextmenu event", async () => {
    render(
      wrap(
        <IssueActionsContextMenu issue={mockIssue}>
          <div data-testid="row">Row</div>
        </IssueActionsContextMenu>,
      ),
    );

    fireEvent.contextMenu(screen.getByTestId("row"));

    expect(await screen.findByText("Status")).toBeInTheDocument();
    expect(screen.getByText("Delete issue")).toBeInTheDocument();
  });
});
