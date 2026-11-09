// Fixture plugin shaped like the packages built on OpenCode 2.x's
// {id, setup(ctx)} convention, which is where the WorkBuddy package lives:
//
//   provider: { id: "cfg-one", models() {} }   one provider, hook inline
//   config(config) { config.provider.cfg-one.models = {...} }
//
// Its model catalog is only in config(), and its provider.models answers the
// signed-in account's own list, which a probe cannot ask for. A probe that reads
// only provider.models reports zero models for it, which is the defect this
// fixture pins.
export const ConfigModelsPlugin = async () => ({
  config: async (config) => {
    config.provider ??= {};
    config.provider["cfg-one"] = {
      npm: "@ai-sdk/openai-compatible",
      models: {
        "cfg-alpha": { name: "Alpha", limit: { context: 1000000 } },
        "cfg-beta": { name: "Beta", limit: { context: 256000 } },
        "cfg-gamma": { name: "Gamma" },
      },
    };
  },
  provider: {
    id: "cfg-one",
    // Needs the account, so a probe must leave it alone and use config().
    models: async (provider, { auth } = {}) => {
      if (!auth) return provider?.models;
      return {};
    },
  },
  auth: {
    provider: "cfg-one",
    methods: [{ label: "Browser sign-in" }, "API key"],
    // Never called by a probe.
    loader: async () => {
      throw new Error("a probe must not load a credential");
    },
  },
});
