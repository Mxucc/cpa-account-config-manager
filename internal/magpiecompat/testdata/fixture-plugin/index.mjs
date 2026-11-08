// Fixture provider plugin for the sidecar tests. It never touches the network.
export const FixturePlugin = async () => ({
  auth: {
    methods: [{ label: "Device code" }, "API key"],
    login: async () => {
      throw new Error("a probe must not start a login");
    },
  },
  provider: {
    fixture: {
      models: async () => [{ id: "fx-alpha" }, { id: "fx-beta" }, "fx-gamma", { id: "fx-alpha" }],
      usage: async () => ({
        windows: [
          { name: "five_hour", used: 1.5, limit: 10, unit: "USD", reset: "2026-11-08T18:00:00Z" },
          { name: "wallet", balance: 4.25, unit: "USD" },
          { name: "aside-window", used: 0, limit: 0, aside: true },
          { name: "" },
        ],
      }),
      // Present to prove the sidecar never calls it.
      fetch: async () => {
        throw new Error("fetch must not be called");
      },
    },
  },
});
