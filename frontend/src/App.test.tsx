import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import App from "./App";

describe("App", () => {
  it("renders the login form", () => {
    render(<App />);
    expect(screen.getByText("LLM API Gateway")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Username")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Invite code (register only)")).toBeInTheDocument();
    expect(screen.getByText("Login")).toBeInTheDocument();
  });
});